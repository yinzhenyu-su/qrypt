package sftp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
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
	readDir                                                                   func(context.Context, *sftp.Client, string) ([]os.FileInfo, error)
	limiter                                                                   *drive.BandwidthLimiter
	metrics                                                                   *driverutil.Buffer
	mu                                                                        sync.RWMutex
	sessionMu                                                                 sync.Mutex
	sessionStoreMu                                                            sync.Mutex
	sessions                                                                  *session.Index
	sessionCancel                                                             context.CancelFunc
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
	return &Driver{address: opts.Address, username: opts.Username, password: opts.Password, privateKey: opts.PrivateKey, passphrase: opts.Passphrase, knownHosts: opts.KnownHosts, rootPath: root, metrics: driverutil.NewBuffer(500), readDir: readDirContext}
}

func readDirContext(ctx context.Context, client *sftp.Client, parent string) ([]os.FileInfo, error) {
	return client.ReadDirContext(ctx, parent)
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) Init(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	connection, client, err := d.connect(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.connection, d.client = connection, client
	d.mu.Unlock()
	return nil
}

const (
	connectTimeout         = 10 * time.Second
	reconnectAttempts      = 3
	reconnectBaseBackoff   = 100 * time.Millisecond
	connectionProbeTimeout = 2 * time.Second
)

func (d *Driver) connect(ctx context.Context) (*ssh.Client, *sftp.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	auth, err := d.authMethod()
	if err != nil {
		return nil, nil, fmt.Errorf("sftp: parse authentication: %w", err)
	}
	hostKeyCallback, err := d.hostKeyCallback()
	if err != nil {
		return nil, nil, fmt.Errorf("sftp: configure host key verification: %w", err)
	}
	config := &ssh.ClientConfig{User: d.username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: hostKeyCallback, Timeout: connectTimeout}
	connection, err := ssh.Dial("tcp", d.address, config)
	if err != nil {
		return nil, nil, fmt.Errorf("sftp: connect %s: %w", d.address, err)
	}
	client, err := sftp.NewClient(connection, sftp.UseConcurrentReads(false))
	if err != nil {
		_ = connection.Close()
		return nil, nil, fmt.Errorf("sftp: create client: %w", err)
	}
	if _, err := client.Stat(d.rootPath); err != nil {
		_ = client.Close()
		_ = connection.Close()
		return nil, nil, fmt.Errorf("sftp: stat root %q: %w", d.rootPath, classifyError(err))
	}
	return connection, client, nil
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
	d.sessionStoreMu.Lock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessionStoreMu.Unlock()
	if d.sessions != nil {
		_ = d.sessions.Flush()
	}
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	d.mu.Lock()
	client, connection := d.client, d.connection
	d.client, d.connection = nil, nil
	d.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if connection != nil {
		if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
	}
	return nil
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.installSessionIndex(store)
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
	if _, err := d.statClient(ctx, client, d.rootPath); err == nil {
		return client, nil
	} else if !isConnectionFailure(err) {
		return nil, fmt.Errorf("sftp: session health check: %w", classifyError(err))
	} else if reconnected, reconnectErr := d.reconnect(ctx, client); reconnectErr == nil {
		return reconnected, nil
	} else {
		return nil, reconnectErr
	}
}

func (d *Driver) statClient(ctx context.Context, client *sftp.Client, name string) (os.FileInfo, error) {
	info, err := statWithContext(ctx, client, name)
	if err != nil && ctx.Err() != nil {
		d.closeIfUnresponsive(client)
	}
	return info, err
}

func statWithContext(ctx context.Context, client *sftp.Client, name string) (os.FileInfo, error) {
	result := make(chan struct {
		info os.FileInfo
		err  error
	}, 1)
	go func() {
		info, err := client.Stat(name)
		result <- struct {
			info os.FileInfo
			err  error
		}{info: info, err: err}
	}()
	select {
	case result := <-result:
		return result.info, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// closeIfUnresponsive closes the shared connection only after a probe confirms it is dead; a slow-but-alive link must not be torn down as collateral damage of a single operation's timeout.
func (d *Driver) closeIfUnresponsive(client *sftp.Client) {
	if !d.connectionAlive(client) {
		d.closeConnection(client)
	}
}

func (d *Driver) connectionAlive(client *sftp.Client) bool {
	probeCtx, cancel := context.WithTimeout(context.Background(), connectionProbeTimeout)
	defer cancel()
	info, err := statWithContext(probeCtx, client, d.rootPath)
	return err == nil && info != nil
}

func (d *Driver) closeConnection(client *sftp.Client) {
	d.mu.RLock()
	connection := d.connection
	current := d.client
	d.mu.RUnlock()
	if current == client && connection != nil {
		_ = connection.Close()
	}
}

func (d *Driver) reconnect(ctx context.Context, failed *sftp.Client) (*sftp.Client, error) {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()

	d.mu.RLock()
	current := d.client
	d.mu.RUnlock()
	if current == nil {
		return nil, fmt.Errorf("sftp: driver is not initialized")
	}
	if current != failed {
		return current, nil
	}

	var lastErr error
	for attempt := 0; attempt < reconnectAttempts; attempt++ {
		if attempt > 0 {
			delay := reconnectBaseBackoff << (attempt - 1)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		connection, client, err := d.connect(ctx)
		if err == nil {
			d.mu.Lock()
			oldClient, oldConnection := d.client, d.connection
			d.connection, d.client = connection, client
			d.mu.Unlock()
			if oldClient != nil {
				_ = oldClient.Close()
			}
			if oldConnection != nil {
				_ = oldConnection.Close()
			}
			return client, nil
		}
		lastErr = err
		if !isConnectionFailure(err) {
			break
		}
	}
	return nil, fmt.Errorf("sftp: reconnect after connection loss: %w", lastErr)
}

func isConnectionFailure(err error) bool {
	if err == nil {
		return false
	}
	category := drive.ErrorCategory(err)
	return (drive.RetryableCategory(category) && (category == drive.ErrorCategoryNetwork || category == drive.ErrorCategoryTimeout)) || drive.ErrorCategoryMessage(err.Error()) == drive.ErrorCategoryNetwork
}

func classifyError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %w", drive.ErrNotFound, err)
	}
	return err
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)
