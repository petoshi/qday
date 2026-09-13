// qday is a standalone Sia-derived QDAY node and native CPU wallet.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/v2/internal/localapp"
	"go.sia.tech/walletd/v2/internal/portmap"
	"go.sia.tech/walletd/v2/persist/sqlite"
	"go.sia.tech/walletd/v2/qday"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap"
)

var version = "0.8.0"

func loadManifest(path string) (m chain.QdayManifest, err error) {
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return m, errors.New("trailing manifest content")
	}
	return m, m.Validate()
}

func writeExclusive(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

func genesis(args []string) error {
	f := flag.NewFlagSet("qday genesis", flag.ContinueOnError)
	address := f.String("address", "", "owner's qday1 address for the fixed 500000-QDAY premine")
	timestamp := f.String("timestamp", time.Now().UTC().Truncate(time.Second).Format(time.RFC3339), "genesis timestamp in RFC3339 (default: current UTC time)")
	message := f.String("message", "", "optional public genesis inscription, at most 256 UTF-8 bytes")
	from := f.String("from", "", "existing mainnet manifest whose premine address should be preserved")
	out := f.String("out", "qday-mainnet.json", "manifest path (must not exist)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *from != "" {
		if *address != "" {
			return errors.New("choose --address or --from")
		}
		previous, err := loadManifest(*from)
		if err != nil {
			return err
		}
		if previous.Development {
			return errors.New("--from requires a mainnet manifest")
		}
		*address = previous.Premine.String()
		if *message == "" {
			*message = previous.GenesisMessage
		}
	}
	if *address == "" {
		return errors.New("genesis requires --address or --from; no mainnet key is generated automatically")
	}
	k, err := types.ParseQdayAddress(*address)
	if err != nil {
		return err
	}
	ts, err := time.Parse(time.RFC3339, *timestamp)
	if err != nil {
		return err
	}
	m, err := chain.NewQdayManifestWithMessage(k, ts, false, *message)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err = writeExclusive(*out, append(b, '\n'), 0644); err != nil {
		return err
	}
	premine := m.Genesis.Transactions[0].SiacoinOutputs[0].Value
	total := premine.Add(m.Network.Qday.Reward.Mul64(m.Network.Qday.MiningBlocks))
	fmt.Printf("QDAY mainnet manifest: %s\nGenesis: %s\nPremine address: %s\nPremine: %s / %s QDAY before QDAY (fixed 500000-coin premine)\n", *out, m.Genesis.ID(), m.Premine, qday.FormatAmount(premine, types.HastingsPerSiacoin), qday.FormatAmount(total, types.HastingsPerSiacoin))
	if m.GenesisMessage != "" {
		fmt.Printf("Genesis message: %s\n", m.GenesisMessage)
	}
	return nil
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "address" {
		if len(args) != 2 {
			return errors.New("usage: qday address <public-address>")
		}
		address, err := types.ParseQdayAddress(args[1])
		if err != nil {
			return err
		}
		fmt.Println(address)
		return nil
	}
	if len(args) == 1 && args[0] == "challenge" {
		fmt.Printf("Mainnet Edwards25519 challenge: %x\nWitness: canonical nonzero scalar x (32 little-endian bytes), x*G = challenge\n", consensus.QdayCanary())
		return nil
	}
	if len(args) > 0 && args[0] == "genesis" {
		return genesis(args[1:])
	}
	f := flag.NewFlagSet("qday", flag.ContinueOnError)
	network := f.String("network", "", "validated mainnet manifest JSON")
	data := f.String("data", "", "data directory (defaults to OS config directory/qday/genesis-ID)")
	httpAddr := f.String("http", "", "loopback wallet API (default 127.0.0.1:19770)")
	p2pAddr := f.String("p2p", "", "P2P listen address (default :19771); auto remembers a random free port")
	advertise := f.String("advertise", "", "public P2P host:port for seed nodes")
	peers := f.String("peers", "", "comma-separated ordinary QDAY peers to remember")
	seeds := f.String("seeds", strings.Join(localapp.MainnetSeeds(), ","), "comma-separated bootstrap peers; empty disables DNS seeds")
	upnp := f.Bool("upnp", true, "automatically map only the P2P TCP port on a UPnP router")
	seedNode := f.Bool("seed-node", false, "serve bootstrap clients; maintain eight ordinary outbound links plus the server peers in --peers")
	desktopManaged := f.Bool("desktop-managed", false, "allow restart requests supervised by QDAY Wallet")
	showVersion := f.Bool("version", false, "print version")
	validateOnly := f.Bool("validate", false, "validate the network manifest and print genesis without starting a node")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("QDAY", version)
		return nil
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *network == "" {
		return errors.New("--network <qday-mainnet.json> is required")
	}
	m, err := loadManifest(*network)
	if err != nil {
		return err
	}
	if m.Development {
		return errors.New("only mainnet consensus manifests are accepted")
	}
	if *validateOnly {
		fmt.Printf("QDAY network validated: %s\nGenesis: %s\n", m.Network.Name, m.Genesis.ID())
		return nil
	}
	apiPort, p2pPort := localapp.MainnetAPIPort, localapp.MainnetP2PPort
	if *httpAddr == "" {
		*httpAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort))
	}
	if *p2pAddr == "" {
		*p2pAddr = net.JoinHostPort("", strconv.Itoa(p2pPort))
	}
	var seedsExplicit, peersExplicit bool
	f.Visit(func(option *flag.Flag) {
		seedsExplicit = seedsExplicit || option.Name == "seeds"
		peersExplicit = peersExplicit || option.Name == "peers"
	})
	if *seedNode {
		if !seedsExplicit {
			*seeds = ""
		}
		if !peersExplicit {
			*peers = strings.Join(localapp.MainnetSeeds(), ",")
		}
	}
	peerList, err := parsePeers(*peers)
	if err != nil {
		return err
	}
	seedList, err := parsePeers(*seeds)
	if err != nil {
		return err
	}
	if *seedNode && len(seedList) != 0 {
		return errors.New("seed nodes use --peers for server links, not --seeds")
	}
	host, _, err := net.SplitHostPort(*httpAddr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("wallet HTTP must listen on loopback")
	}
	if *data == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		id := m.Genesis.ID().String()
		*data = filepath.Join(dir, "qday", m.Network.Name+"-"+id[:12])
	}
	if err = localapp.PrivateDirectory(*data); err != nil {
		return err
	}
	log, err := zap.NewProduction()
	if err != nil {
		return err
	}
	defer log.Sync()
	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(*data, "consensus.db"))
	if err != nil {
		return err
	}
	defer bdb.Close()
	db, tip, err := chain.NewDBStore(bdb, &m.Network, m.Genesis, nil)
	if err != nil {
		return err
	}
	cm := chain.NewManager(db, tip, chain.WithLog(log.Named("chain")))
	if index, ok := cm.BestIndex(0); !ok || index.ID != m.Genesis.ID() {
		return errors.New("data directory belongs to another genesis")
	}
	store, err := sqlite.OpenDatabase(filepath.Join(*data, "wallet.sqlite3"), sqlite.WithLog(log.Named("walletdb")))
	if err != nil {
		return err
	}
	defer store.Close()
	for _, peer := range append(peerList, seedList...) {
		if err = store.AddPeer(peer); err != nil {
			return err
		}
	}
	ps, err := sqlite.NewPeerStore(store)
	if err != nil {
		return err
	}
	l, mappingOwner, err := localapp.ListenP2P(*data, *p2pAddr)
	if err != nil {
		return err
	}
	defer l.Close()
	addr := *advertise
	if addr == "" {
		_, p, _ := net.SplitHostPort(l.Addr().String())
		addr = net.JoinHostPort("127.0.0.1", p)
	}
	if _, err = parsePeers(addr); err != nil {
		return err
	}
	options := []syncer.Option{syncer.WithLogger(log.Named("p2p")), syncer.WithBootstrapPeers(seedList)}
	if *seedNode {
		options = append(options, syncer.WithSeedMode(peerList))
	}
	sy := syncer.New(l, cm, ps, gateway.Header{GenesisID: m.Genesis.ID(), UniqueID: gateway.GenerateUniqueID(), NetAddress: addr}, options...)
	defer sy.Close()
	go sy.Run()
	wm, err := wallet.NewManager(cm, store, wallet.WithLogger(log.Named("wallet")), wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		return err
	}
	defer wm.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	svc, err := qday.NewService(ctx, *data, cm, wm, sy, m)
	if err != nil {
		return err
	}
	defer svc.Close()
	svc.RestartEnabled = *desktopManaged
	svc.PortMapping = portmap.Start(ctx, l.Addr(), mappingOwner, *upnp && *advertise == "")
	defer svc.PortMapping.Close()
	tokenPath := filepath.Join(*data, "api.token")
	tokenBytes, err := os.ReadFile(tokenPath)
	if errors.Is(err, os.ErrNotExist) {
		var entropy [32]byte
		rand.Read(entropy[:])
		tokenBytes = []byte(hex.EncodeToString(entropy[:]))
		if err = writeExclusive(tokenPath, tokenBytes, 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	token := strings.TrimSpace(string(tokenBytes))
	if raw, err := hex.DecodeString(token); err != nil || len(raw) != 32 {
		return errors.New("invalid local api.token; expected 32 random bytes encoded as hex")
	}
	hl, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		return err
	}
	defer hl.Close()
	h := svc.Handler(token, hl.Addr().String())
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	errc := make(chan error, 1)
	go func() { errc <- server.Serve(hl) }()
	// The desktop launcher discovers this ephemeral port after the API starts.
	// Discovery is only provided for the desktop's literal loopback listener.
	if host, _, _ := net.SplitHostPort(hl.Addr().String()); host == "127.0.0.1" {
		if err = localapp.WriteEndpoint(*data, localapp.Endpoint{Format: 1, URL: "http://" + hl.Addr().String(), PID: os.Getpid(), Genesis: m.Genesis.ID()}); err != nil {
			return err
		}
		defer os.Remove(filepath.Join(*data, "node.json"))
	}
	fmt.Printf("QDAY %s · %s\nWallet: http://%s\nLocal access token file: %s\nP2P: %s\nGenesis: %s\n", version, m.Network.Name, hl.Addr(), tokenPath, l.Addr(), m.Genesis.ID())
	select {
	case <-svc.Done():
		if svc.RestartRequested() {
			return localapp.ErrRestart
		}
		return nil
	case <-ctx.Done():
		return nil
	case err = <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func parsePeers(value string) ([]string, error) {
	var peers []string
	for _, addr := range strings.Split(value, ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		host, port, err := net.SplitHostPort(addr)
		n, portErr := strconv.Atoi(port)
		if err != nil || host == "" || len(host) > 253 || portErr != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid QDAY peer address: %q", addr)
		}
		peers = append(peers, addr)
	}
	return peers, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, localapp.ErrRestart) {
			os.Exit(localapp.RestartExitCode)
		}
		fmt.Fprintln(os.Stderr, "qday:", err)
		os.Exit(1)
	}
}
