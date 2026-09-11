// QDAY Wallet launches the native node and its embedded browser interface.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/v2/internal/localapp"
)

var version = "0.6.0"

type distribution struct {
	Format  int      `json:"format"`
	Network string   `json:"network"`
	Peers   []string `json:"peers"`
	Seeds   []string `json:"seeds"`
}

type options struct {
	data, node, network, browser string
	noBrowser                    bool
	peers                        []string
	p2p, advertise               string
	upnp                         bool
}

func networkFor(o *options, resourceDir string) (chain.QdayManifest, error) {
	o.peers = localapp.MainnetSeeds()
	if o.network == "" {
		b, err := os.ReadFile(filepath.Join(resourceDir, "distribution.json"))
		if err != nil {
			return chain.QdayManifest{}, errors.New("distribution.json is missing; extract the complete QDAY Wallet archive before launching")
		}
		var d distribution
		if json.Unmarshal(b, &d) != nil || d.Format != 1 {
			return chain.QdayManifest{}, errors.New("invalid distribution configuration")
		}
		if d.Network != "mainnet" {
			return chain.QdayManifest{}, errors.New("only mainnet wallet distributions are accepted")
		}
		o.network = filepath.Join(resourceDir, "qday-mainnet.json")
		// Older distributions named their bootstrap list "peers".
		if d.Seeds != nil || d.Peers != nil {
			o.peers = append(d.Seeds, d.Peers...)
		}
	}
	b, err := os.ReadFile(o.network)
	if err != nil {
		return chain.QdayManifest{}, err
	}
	if len(b) > 1<<20 {
		return chain.QdayManifest{}, errors.New("invalid network manifest size")
	}
	var m chain.QdayManifest
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, err
	}
	if d.Decode(new(any)) != io.EOF {
		return m, errors.New("trailing network manifest data")
	}
	if m.Development {
		return m, errors.New("mainnet distribution contains a development manifest")
	}
	return m, m.Validate()
}

// Ignore proxy variables and redirects: a local token must never leave loopback.
var localClient = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("local node redirect refused") }}

func call(e localapp.Endpoint, token, path string, body any, result any) error {
	if err := e.Validate(); err != nil {
		return err
	}
	method := "GET"
	var data io.Reader
	if body != nil {
		method = "POST"
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.URL+"/api/"+path, data)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := localClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("local node returned HTTP %d", res.StatusCode)
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(result)
}

func endpoint(dir string, genesis types.BlockID) (e localapp.Endpoint, token string, err error) {
	e, err = localapp.ReadEndpoint(dir)
	if err != nil {
		return
	}
	if e.Genesis != genesis {
		err = errors.New("data directory belongs to another QDAY network")
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "api.token"))
	if err != nil {
		return e, "", err
	}
	token = strings.TrimSpace(string(b))
	if len(token) != 64 {
		return e, "", errors.New("invalid local credential")
	}
	var state struct {
		Genesis types.BlockID `json:"genesis"`
	}
	if err = call(e, token, "status", nil, &state); err == nil && state.Genesis != genesis {
		err = errors.New("local node genesis mismatch")
	}
	return
}

func openWallet(e localapp.Endpoint, token string, o options) error {
	if o.noBrowser {
		return nil
	}
	var result struct {
		URL string `json:"url"`
	}
	if err := call(e, token, "launch", struct{}{}, &result); err != nil {
		return err
	}
	u, err := url.Parse(result.URL)
	if err != nil || u.Scheme+"://"+u.Host != e.URL || u.Path != "/" || u.RawQuery != "" || !strings.HasPrefix(u.Fragment, "launch=") || len(u.Fragment) != 71 {
		return errors.New("invalid browser launch URL")
	}
	if o.browser != "" {
		// An explicit browser executable is useful for alternative browsers and
		// automated tests. It is never passed to a command shell.
		cmd := exec.Command(o.browser, result.URL)
		hideWindow(cmd)
		if err = cmd.Start(); err != nil {
			return err
		}
		go cmd.Wait()
		return nil
	}
	return openBrowser(result.URL)
}

func run(args []string) (noDialog bool, err error) {
	var o options
	f := flag.NewFlagSet("QDAY Wallet", flag.ContinueOnError)
	f.StringVar(&o.data, "data", "", "optional wallet data directory")
	f.StringVar(&o.node, "node", "", "optional native node executable")
	f.StringVar(&o.network, "network", "", "mainnet manifest override")
	f.StringVar(&o.browser, "browser", "", "browser executable override")
	f.BoolVar(&o.noBrowser, "no-browser", false, "unattended launcher, without opening a browser or dialogs")
	f.StringVar(&o.p2p, "p2p", "auto", "P2P listener; auto remembers a random free port")
	f.StringVar(&o.advertise, "advertise", "", "external P2P host:port for manual router or VPN forwarding")
	f.BoolVar(&o.upnp, "upnp", true, "automatically map the P2P TCP port using UPnP")
	seeds := f.String("seeds", "", "override the bootstrap list; empty disables DNS seeds")
	showVersion := f.Bool("version", false, "print version")
	if err = f.Parse(args); err != nil {
		return true, err
	}
	if *showVersion {
		fmt.Println("QDAY Wallet", version)
		return true, nil
	}
	if f.NArg() != 0 {
		return o.noBrowser, errors.New("unexpected launcher arguments")
	}
	executable, err := os.Executable()
	if err != nil {
		return o.noBrowser, err
	}
	resources := filepath.Join(filepath.Dir(executable), "resources")
	m, err := networkFor(&o, resources)
	if err != nil {
		return o.noBrowser, err
	}
	if o.data == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return o.noBrowser, err
		}
		o.data = filepath.Join(base, "qday", m.Network.Name+"-"+m.Genesis.ID().String()[:12])
	}
	o.data, err = filepath.Abs(o.data)
	if err != nil {
		return o.noBrowser, err
	}
	if err = localapp.PrivateDirectory(o.data); err != nil {
		return o.noBrowser, err
	}
	if e, token, err := endpoint(o.data, m.Genesis.ID()); err == nil {
		return o.noBrowser, openWallet(e, token, o)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	lock, err := os.OpenFile(filepath.Join(o.data, "launcher.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return o.noBrowser, err
	}
	defer lock.Close()
	if err = lockFile(lock); err != nil {
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return o.noBrowser, nil
			case <-deadline.C:
				return o.noBrowser, errors.New("QDAY is already starting; reopen the wallet shortly")
			case <-ticker.C:
				if e, token, err := endpoint(o.data, m.Genesis.ID()); err == nil {
					return o.noBrowser, openWallet(e, token, o)
				}
			}
		}
	}
	if e, token, err := endpoint(o.data, m.Genesis.ID()); err == nil {
		return o.noBrowser, openWallet(e, token, o)
	}
	if o.node == "" {
		o.node = filepath.Join(resources, "qday-node")
		if runtime.GOOS == "windows" {
			o.node += ".exe"
		}
	}
	if info, err := os.Stat(o.node); err != nil || info.IsDir() {
		return o.noBrowser, errors.New("native node is missing; extract the complete QDAY Wallet archive")
	}
	logPath := filepath.Join(o.data, "node.log")
	if stat, err := os.Stat(logPath); err == nil && stat.Size() > 8<<20 {
		_ = os.Remove(logPath + ".old")
		_ = os.Rename(logPath, logPath+".old")
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return o.noBrowser, err
	}
	defer log.Close()
	nodeArgs := []string{"--data", o.data, "--http", "127.0.0.1:0", "--p2p", o.p2p, fmt.Sprintf("--upnp=%t", o.upnp)}
	if o.advertise != "" {
		nodeArgs = append(nodeArgs, "--advertise", o.advertise)
	}
	f.Visit(func(option *flag.Flag) {
		if option.Name == "seeds" {
			o.peers = strings.Split(*seeds, ",")
		}
	})
	nodeArgs = append(nodeArgs, "--network", o.network)
	nodeArgs = append(nodeArgs, "--seeds", strings.Join(o.peers, ","))
	nodeArgs = append(nodeArgs, "--desktop-managed")
	for {
		err = runNode(ctx, o, nodeArgs, log, m.Genesis.ID())
		var exitErr *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exitErr) && exitErr.ExitCode() == localapp.RestartExitCode {
			continue
		}
		return o.noBrowser, err
	}
}

// runNode owns one native process. A requested restart returns only after all
// databases, network listeners and CPU work in that process have closed.
func runNode(ctx context.Context, o options, nodeArgs []string, log *os.File, genesis types.BlockID) (err error) {
	cmd := exec.Command(o.node, nodeArgs...)
	cmd.Stdout = log
	cmd.Stderr = log
	hideWindow(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var e localapp.Endpoint
	var token string
	finished := false
	defer func() {
		if finished {
			return
		}
		_ = call(e, token, "shutdown", struct{}{}, nil)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
ready:
	for {
		select {
		case <-ctx.Done():
			return nil
		case err = <-done:
			finished = true
			if err == nil {
				err = errors.New("native node stopped during startup")
			}
			return fmt.Errorf("QDAY startup failed; see %s: %w", log.Name(), err)
		case <-deadline.C:
			return fmt.Errorf("QDAY startup timed out; another node may be using this data directory. See %s", log.Name())
		case <-ticker.C:
			e, token, err = endpoint(o.data, genesis)
			if err == nil && e.PID == cmd.Process.Pid {
				break ready
			}
		}
	}
	if err = openWallet(e, token, o); err != nil {
		return fmt.Errorf("could not open the wallet in your browser: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case err = <-done:
		finished = true
		return err
	}
}

func main() {
	noDialog, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "QDAY Wallet:", err)
		if !noDialog {
			showError(err.Error())
		}
		os.Exit(1)
	}
}
