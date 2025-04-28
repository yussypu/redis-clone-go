package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/tidwall/resp"
)

const defaultListenAddr = ":5001"

type Config struct {
	ListenAddr string
}

type Message struct {
	cmd  Command
	peer *Peer
}

type Server struct {
	Config
	peers     map[*Peer]bool
	ln        net.Listener
	addPeerCh chan *Peer
	delPeerCh chan *Peer
	quitCh    chan struct{}
	msgCh     chan Message

	kv *KV
}

func NewServer(cfg Config) *Server {
	if len(cfg.ListenAddr) == 0 {
		cfg.ListenAddr = defaultListenAddr
	}
	return &Server{
		Config:    cfg,
		peers:     make(map[*Peer]bool),
		addPeerCh: make(chan *Peer),
		delPeerCh: make(chan *Peer),
		quitCh:    make(chan struct{}),
		msgCh:     make(chan Message),
		kv:        NewKV(),
	}
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.ListenAddr)
	if err != nil {
		return err
	}
	s.ln = ln

	go s.loop()

	slog.Info("goredis server running", "listenAddr", s.ListenAddr)

	return s.acceptLoop()
}

func (s *Server) handleMessage(msg Message) error {
	switch v := msg.cmd.(type) {
	case ClientCommand:
		if err := resp.NewWriter(msg.peer.conn).WriteString("OK"); err != nil {
			return err
		}
	case SetCommand:
		if err := s.kv.Set(v.key, v.val); err != nil {
			return err
		}
		if err := resp.NewWriter(msg.peer.conn).WriteString("OK"); err != nil {
			return err
		}
	case GetCommand:
		val, ok := s.kv.Get(v.key)
		if !ok {
			return fmt.Errorf("key not found")
		}
		if err := resp.NewWriter(msg.peer.conn).WriteString(string(val)); err != nil {
			return err
		}
	case HelloCommand:
		spec := map[string]string{
			"server": "redis",
		}
		_, err := msg.peer.Send(respWriteMap(spec))
		if err != nil {
			return fmt.Errorf("peer send error: %s", err)
		}
	}
	return nil
}

func (s *Server) loop() {
	for {
		select {
		case msg := <-s.msgCh:
			if err := s.handleMessage(msg); err != nil {
				slog.Error("raw message error", "err", err)
			}
		case <-s.quitCh:
			return
		case peer := <-s.addPeerCh:
			slog.Info("peer connected", "remoteAddr", peer.conn.RemoteAddr())
			s.peers[peer] = true
		case peer := <-s.delPeerCh:
			slog.Info("peer disconnected", "remoteAddr", peer.conn.RemoteAddr())
			delete(s.peers, peer)
		}
	}
}

func (s *Server) acceptLoop() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			slog.Error("accept error", "err", err)
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	peer := NewPeer(conn, s.msgCh, s.delPeerCh)
	s.addPeerCh <- peer
	if err := peer.readLoop(); err != nil {
		slog.Error("peer read error", "err", err, "remoteAddr", conn.RemoteAddr())
	}
}

// ------------------------
// CLI MODE
// ------------------------

func startCLI() {
	kv := NewKV()
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Welcome to YahyaRedis CLI 💥")
	fmt.Println("Type HELP for commands.")

	for {
		fmt.Print("> ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if len(input) == 0 {
			continue
		}

		args := strings.SplitN(input, " ", 3)
		cmd := strings.ToUpper(args[0])

		switch cmd {
		case "SET":
			if len(args) < 3 {
				fmt.Println("Usage: SET <key> <value>")
				continue
			}
			key := []byte(args[1])
			val := []byte(args[2])
			if err := kv.Set(key, val); err != nil {
				fmt.Println("Error:", err)
			} else {
				fmt.Println("OK")
			}

		case "GET":
			if len(args) < 2 {
				fmt.Println("Usage: GET <key>")
				continue
			}
			key := []byte(args[1])
			val, ok := kv.Get(key)
			if !ok {
				fmt.Println("(nil)")
			} else {
				fmt.Println(string(val))
			}

		case "HELP":
			fmt.Println("Available commands:")
			fmt.Println("  SET <key> <value>  - store a value")
			fmt.Println("  GET <key>          - retrieve a value")
			fmt.Println("  EXIT               - exit the program")

		case "EXIT":
			fmt.Println("Bye 👋")
			return

		default:
			fmt.Println("Unknown command. Type HELP for a list of commands.")
		}
	}
}

// ------------------------
// Main
// ------------------------

func main() {
	listenAddr := flag.String("listenAddr", defaultListenAddr, "listen address of the goredis server")
	cliMode := flag.Bool("cli", false, "start in CLI mode instead of server mode")
	flag.Parse()

	if *cliMode {
		startCLI()
		return
	}

	server := NewServer(Config{
		ListenAddr: *listenAddr,
	})
	log.Fatal(server.Start())
}
