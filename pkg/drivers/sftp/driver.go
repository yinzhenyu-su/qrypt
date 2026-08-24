package sftp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Driver struct {
	drive.UnsupportedOperations
	address, username, password, privateKey, passphrase, knownHosts, rootPath string
	client                                                                    *sftp.Client
	connection                                                                *ssh.Client
	stateStore                                                                drive.StateStore
	limiter                                                                   *drive.BandwidthLimiter
	metrics                                                                   *driverutil.Buffer
	mu                                                                        sync.RWMutex
}

type Options struct {
	Address, Username, Password, PrivateKey, Passphrase, KnownHosts, RootPath string
}

func init() {
	drive.Register("sftp", func(params drive.Params) (drive.Driver, error) {
		address, username := params["address"], params["username"]
		if address == "" {
			return nil, fmt.Errorf("sftp: missing address")
		}
		if username == "" {
			return nil, fmt.Errorf("sftp: missing username")
		}
		if params["password"] == "" && params["private_key"] == "" {
			return nil, fmt.Errorf("sftp: one of password or private_key is required")
		}
		return New(Options{Address: address, Username: username, Password: params["password"], PrivateKey: params["private_key"], Passphrase: params["passphrase"], KnownHosts: params["known_hosts"], RootPath: params["root_path"]}), nil
	},
		drive.ParamDef{Name: "address", Required: true, Description: "SFTP server address, including port", Example: "sftp.example.com:22"},
		drive.ParamDef{Name: "username", Required: true, Description: "SFTP username", Example: "user"},
		drive.ParamDef{Name: "password", Secret: true, Description: "SFTP password", Example: "your-password"},
		drive.ParamDef{Name: "private_key", Secret: true, Description: "Path to or PEM-encoded SSH private key", Example: "~/.ssh/id_ed25519"},
		drive.ParamDef{Name: "passphrase", Secret: true, Description: "SSH private key passphrase"},
		drive.ParamDef{Name: "known_hosts", Description: "OpenSSH known_hosts file used to verify the SFTP server; when omitted, ~/.ssh/known_hosts is used if present", Example: "~/.ssh/known_hosts"},
		drive.ParamDef{Name: "root_path", Description: "Remote directory used as this mount root", Default: "/", Example: "/data/qrypt"},
	)
}

func New(opts Options) *Driver {
	root := path.Clean(opts.RootPath)
	if root == "." || root == "" {
		root = "/"
	}
	return &Driver{address: opts.Address, username: opts.Username, password: opts.Password, privateKey: opts.PrivateKey, passphrase: opts.Passphrase, knownHosts: opts.KnownHosts, rootPath: root, metrics: driverutil.NewBuffer(500)}
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) Init(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	auth, err := d.authMethod()
	if err != nil {
		return fmt.Errorf("sftp: parse authentication: %w", err)
	}
	hostKeyCallback, err := d.hostKeyCallback()
	if err != nil {
		return fmt.Errorf("sftp: configure host key verification: %w", err)
	}
	config := &ssh.ClientConfig{User: d.username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: hostKeyCallback, Timeout: 10 * time.Second}
	connection, err := ssh.Dial("tcp", d.address, config)
	if err != nil {
		return fmt.Errorf("sftp: connect %s: %w", d.address, err)
	}
	client, err := sftp.NewClient(connection)
	if err != nil {
		connection.Close()
		return fmt.Errorf("sftp: create client: %w", err)
	}
	if _, err := client.Stat(d.rootPath); err != nil {
		client.Close()
		connection.Close()
		return fmt.Errorf("sftp: stat root %q: %w", d.rootPath, classifyError(err))
	}
	d.mu.Lock()
	d.connection, d.client = connection, client
	d.mu.Unlock()
	return nil
}

func (d *Driver) hostKeyCallback() (ssh.HostKeyCallback, error) {
	knownHostsPath := util.ExpandHome(d.knownHosts)
	if knownHostsPath == "" {
		defaultPath := util.ExpandHome("~/.ssh/known_hosts")
		if _, err := os.Stat(defaultPath); err == nil {
			knownHostsPath = defaultPath
		}
	}
	if knownHostsPath == "" {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("read known_hosts %q: %w", knownHostsPath, err)
	}
	return callback, nil
}

func (d *Driver) Drop(ctx context.Context) error {
	d.mu.Lock()
	client, connection := d.client, d.connection
	d.client, d.connection = nil, nil
	d.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if connection != nil {
		return connection.Close()
	}
	return nil
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.pruneUploadSessions()
}

func (d *Driver) authMethod() (ssh.AuthMethod, error) {
	if d.privateKey == "" {
		return ssh.Password(d.password), nil
	}
	privateKey, err := loadPrivateKey(d.privateKey)
	if err != nil {
		return nil, err
	}
	var signer ssh.Signer
	if d.passphrase == "" {
		signer, err = ssh.ParsePrivateKey(privateKey)
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(privateKey, []byte(d.passphrase))
	}
	if err != nil {
		return nil, err
	}
	return ssh.PublicKeys(signer), nil
}

func loadPrivateKey(value string) ([]byte, error) {
	pathValue := util.ExpandHome(value)
	info, err := os.Stat(pathValue)
	if err == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("sftp: private_key path is a directory: %q", pathValue)
		}
		key, readErr := os.ReadFile(pathValue)
		if readErr != nil {
			return nil, fmt.Errorf("sftp: read private_key %q: %w", pathValue, readErr)
		}
		return key, nil
	}
	if strings.Contains(value, "-----BEGIN") {
		return []byte(value), nil
	}
	return nil, fmt.Errorf("sftp: private_key is neither a readable file nor PEM content %q: %w", value, err)
}

func (d *Driver) getClient(ctx context.Context) (*sftp.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.RLock()
	client := d.client
	d.mu.RUnlock()
	if client == nil {
		return nil, fmt.Errorf("sftp: driver is not initialized")
	}
	return client, nil
}

func classifyError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %w", drive.ErrNotFound, err)
	}
	return err
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)
