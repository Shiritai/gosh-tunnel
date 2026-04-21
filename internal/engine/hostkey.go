package engine

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyMu serializes appends to the known_hosts file across goroutines.
var hostKeyMu sync.Mutex

// hostKeyConfig bundles the verification callback with the list of host key
// algorithms already pinned for the target host. Passing the algorithms to
// ssh.ClientConfig.HostKeyAlgorithms forces the server to negotiate a key
// type we have on file — otherwise multi-key servers may pick a different
// algorithm each handshake and trip a spurious mismatch.
type hostKeyConfig struct {
	Callback   ssh.HostKeyCallback
	Algorithms []string
}

type dummyPublicKey struct{}

func (dummyPublicKey) Type() string                        { return "ssh-dummy" }
func (dummyPublicKey) Marshal() []byte                     { return []byte{0} }
func (dummyPublicKey) Verify([]byte, *ssh.Signature) error { return errors.New("dummy") }

type dummyAddr string

func (d dummyAddr) Network() string { return "tcp" }
func (d dummyAddr) String() string  { return string(d) }

// defaultHostKeyAlgos mirrors what OpenSSH / Go ssh will accept when no
// preference is set. Appended after pinned algorithms so that a server which
// no longer offers the pinned type can still negotiate — the callback will
// still enforce a match against known_hosts for whatever key is presented.
var defaultHostKeyAlgos = []string{
	ssh.KeyAlgoED25519,
	ssh.CertAlgoED25519v01,
	ssh.KeyAlgoRSASHA512,
	ssh.KeyAlgoRSASHA256,
	ssh.CertAlgoRSASHA512v01,
	ssh.CertAlgoRSASHA256v01,
	ssh.KeyAlgoECDSA256,
	ssh.KeyAlgoECDSA384,
	ssh.KeyAlgoECDSA521,
	ssh.CertAlgoECDSA256v01,
	ssh.CertAlgoECDSA384v01,
	ssh.CertAlgoECDSA521v01,
	ssh.KeyAlgoRSA,
}

// preferredAlgorithms returns a HostKeyAlgorithms list that puts the types
// already pinned in known_hosts first (so the server prefers a key we can
// verify locally), then appends the standard default set as fallbacks. This
// matches OpenSSH semantics where HostKeyAlgorithms is an ordered preference,
// not a whitelist — the callback still rejects any key that fails to match.
func preferredAlgorithms(verify ssh.HostKeyCallback, hostPort string) []string {
	var pinned []string
	err := verify(hostPort, dummyAddr(hostPort), dummyPublicKey{})
	if err != nil {
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			for _, k := range keyErr.Want {
				pinned = append(pinned, k.Key.Type())
			}
		}
	}

	out := make([]string, 0, len(pinned)+len(defaultHostKeyAlgos))
	seen := make(map[string]bool)
	for _, a := range pinned {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, a := range defaultHostKeyAlgos {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// hostKeyCallbackFor returns the host key verification config for hostPort.
//   - Verifies remote keys against ~/.ssh/known_hosts (and /etc/ssh/ssh_known_hosts if present).
//   - On unknown host (TOFU): appends the key and accepts, unless strict mode is on.
//   - On key mismatch: always rejects (MITM protection).
//
// Env overrides:
//   GOSH_INSECURE_HOST_KEY=1        -> disable verification entirely (loud warning).
//   GOSH_STRICT_HOST_KEY_CHECKING=1 -> reject unknown hosts instead of TOFU-accepting.
//   GOSH_KNOWN_HOSTS=<path>         -> override the user known_hosts file path.
func hostKeyCallbackFor(hostPort string) (hostKeyConfig, error) {
	if os.Getenv("GOSH_INSECURE_HOST_KEY") == "1" {
		log.Printf("WARNING: GOSH_INSECURE_HOST_KEY=1 set; SSH host key verification is DISABLED. This is unsafe.")
		return hostKeyConfig{Callback: ssh.InsecureIgnoreHostKey()}, nil
	}

	userPath := os.Getenv("GOSH_KNOWN_HOSTS")
	if userPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return hostKeyConfig{}, fmt.Errorf("resolve home dir for known_hosts: %w", err)
		}
		userPath = filepath.Join(home, ".ssh", "known_hosts")
	}

	if err := ensureKnownHostsFile(userPath); err != nil {
		return hostKeyConfig{}, err
	}

	files := []string{userPath}
	if _, err := os.Stat("/etc/ssh/ssh_known_hosts"); err == nil {
		files = append(files, "/etc/ssh/ssh_known_hosts")
	}

	verify, err := knownhosts.New(files...)
	if err != nil {
		return hostKeyConfig{}, fmt.Errorf("load known_hosts: %w", err)
	}

	strict := os.Getenv("GOSH_STRICT_HOST_KEY_CHECKING") == "1"
	algos := preferredAlgorithms(verify, hostPort)

	cb := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			// Unknown host: TOFU path.
			if strict {
				return fmt.Errorf("unknown host %s (strict mode): %w", hostname, err)
			}
			if appendErr := appendKnownHost(userPath, hostname, remote, key); appendErr != nil {
				return fmt.Errorf("trust-on-first-use append failed for %s: %w", hostname, appendErr)
			}
			log.Printf("WARNING: trust-on-first-use accepted host %s (%s); pinned in %s", hostname, key.Type(), userPath)
			return nil
		}
		// Mismatch (len(Want) > 0) or other error: reject.
		return fmt.Errorf("host key verification failed for %s: %w", hostname, err)
	}
	return hostKeyConfig{Callback: cb, Algorithms: algos}, nil
}

func ensureKnownHostsFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat known_hosts: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create known_hosts dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create known_hosts: %w", err)
	}
	return f.Close()
}

func appendKnownHost(path, hostname string, remote net.Addr, key ssh.PublicKey) error {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()

	addrs := []string{knownhosts.Normalize(hostname)}
	if remote != nil {
		if r := knownhosts.Normalize(remote.String()); r != addrs[0] && r != "" {
			addrs = append(addrs, r)
		}
	}
	line := knownhosts.Line(addrs, key) + "\n"

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}
