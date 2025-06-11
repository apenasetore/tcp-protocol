package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Server struct {
	port     string
	sockfd   int
	clients  map[net.Conn]bool
	mutex    sync.RWMutex
	shutdown chan bool
}

func NewServer(port string) *Server {
	return &Server{
		port:     port,
		clients:  make(map[net.Conn]bool),
		shutdown: make(chan bool, 1),
	}
}

func (s *Server) Start() error {
	// Criar socket TCP
	// Cria um file descriptor de socket usando syscall
	// AF_INET6 é usado para IPv6, mas pode ser alterado para AF_INET para IPv4
	sockfd, err := syscall.Socket(syscall.AF_INET6, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("erro ao criar socket: %v", err)
	}
	s.sockfd = sockfd

	// Configurar socket para reutilizar endereço
	// Isso permite que o socket seja reutilizado imediatamente após o fechamento
	// Isso é útil para evitar erros de "endereço já em uso"
	err = syscall.SetsockoptInt(sockfd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("erro ao configurar SO_REUSEADDR: %v", err)
	}

	// Configurar endereço do servidor
	port := 8080
	if s.port != "" {
		fmt.Sscanf(s.port, "%d", &port)
	}

	addr := syscall.SockaddrInet4{
		Port: port,
		Addr: [4]byte{0, 0, 0, 0}, // 0.0.0.0 (todas as interfaces)
	}

	// Fazer bind do socket
	err = syscall.Bind(sockfd, &addr)
	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("erro ao fazer bind na porta %d: %v", port, err)
	}

	// Colocar socket em modo listen
	err = syscall.Listen(sockfd, 128) // backlog de 128 conexões
	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("erro ao colocar socket em listen: %v", err)
	}

	fmt.Printf("Servidor TCP iniciado usando socket na porta %d\n", port)
	fmt.Printf("Socket file descriptor: %d\n", sockfd)
	fmt.Printf("Aguardando conexões...\n\n")

	// Goroutine para aceitar conexões
	go s.acceptConnections()

	// Aguardar sinal de shutdown
	s.waitForShutdown()
	return nil
}

func (s *Server) acceptConnections() {
	for {
		// Accept usando syscall diretamente
		clientFd, clientAddr, err := syscall.Accept(s.sockfd)
		if err != nil {
			select {
			case <-s.shutdown:
				return
			default:
				log.Printf("❌ Erro ao aceitar conexão: %v", err)
				continue
			}
		}

		// Converter socket descriptor para net.Conn
		file := os.NewFile(uintptr(clientFd), "tcp-client")
		conn, err := net.FileConn(file)
		file.Close() // Fechar o file descriptor, mas manter a conexão

		if err != nil {
			log.Printf("❌ Erro ao converter socket para net.Conn: %v", err)
			syscall.Close(clientFd)
			continue
		}

		// Obter endereço do cliente
		var clientAddrStr string
		if addr4, ok := clientAddr.(*syscall.SockaddrInet4); ok {
			clientAddrStr = fmt.Sprintf("%d.%d.%d.%d:%d",
				addr4.Addr[0], addr4.Addr[1], addr4.Addr[2], addr4.Addr[3], addr4.Port)
		} else {
			clientAddrStr = "unknown"
		}

		// Adicionar cliente à lista
		s.mutex.Lock()
		s.clients[conn] = true
		clientCount := len(s.clients)
		s.mutex.Unlock()

		fmt.Printf("✅ Nova conexão estabelecida: %s (FD: %d, Total: %d clientes)\n",
			clientAddrStr, clientFd, clientCount)

		// Tratar cliente em goroutine separada
		go s.handleClient(conn)
	}
}

func (s *Server) handleClient(conn net.Conn) {
	defer func() {
		s.removeClient(conn)
		conn.Close()
	}()

	// Configurar timeout para leitura
	conn.SetReadDeadline(time.Now().Add(5 * time.Minute))

	reader := bufio.NewReader(conn)
	clientAddr := conn.RemoteAddr().String()

	// Enviar mensagem de boas-vindas
	welcome := fmt.Sprintf("🎉 Bem-vindo ao servidor TCP! Você está conectado como %s\n", clientAddr)
	conn.Write([]byte(welcome))

	for {
		// Ler mensagem do cliente
		message, err := reader.ReadString('\n')
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				fmt.Printf("⏰ Timeout na conexão com %s\n", clientAddr)
			} else {
				fmt.Printf("📤 Cliente %s desconectou\n", clientAddr)
			}
			break
		}

		message = strings.TrimSpace(message)
		fmt.Printf("📥 [%s]: %s\n", clientAddr, message)

		// Processar mensagem
		response := s.processMessage(message, clientAddr)

		// Enviar resposta
		_, err = conn.Write([]byte(response + "\n"))
		if err != nil {
			fmt.Printf("❌ Erro ao enviar resposta para %s: %v\n", clientAddr, err)
			break
		}

		// Resetar timeout
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	}
}

func (s *Server) processMessage(message, clientAddr string) string {
	switch strings.ToLower(message) {
	case "ping":
		return "🏓 pong"
	case "time":
		return fmt.Sprintf("🕐 Horário atual: %s", time.Now().Format("15:04:05"))
	case "status":
		s.mutex.RLock()
		clientCount := len(s.clients)
		s.mutex.RUnlock()
		return fmt.Sprintf("📊 Servidor ativo | Clientes conectados: %d", clientCount)
	case "help":
		return `📋 Comandos disponíveis:
- ping: retorna pong
- time: mostra horário atual
- status: mostra status do servidor
- clients: lista clientes conectados
- help: mostra esta ajuda
- quit: desconecta do servidor`
	case "clients":
		s.mutex.RLock()
		var clientList []string
		for client := range s.clients {
			clientList = append(clientList, client.RemoteAddr().String())
		}
		s.mutex.RUnlock()
		return fmt.Sprintf("👥 Clientes conectados: %s", strings.Join(clientList, ", "))
	case "quit":
		return fmt.Sprintf("👋 Até logo, %s! Desconectando...", clientAddr)
	default:
		return fmt.Sprintf("💬 Eco: %s", message)
	}
}

func (s *Server) removeClient(conn net.Conn) {
	s.mutex.Lock()
	delete(s.clients, conn)
	clientCount := len(s.clients)
	s.mutex.Unlock()

	fmt.Printf("❌ Cliente desconectado: %s (Restam: %d clientes)\n",
		conn.RemoteAddr().String(), clientCount)
}

func (s *Server) waitForShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	fmt.Println("\n🛑 Recebido sinal de shutdown...")
	s.Stop()
}

func (s *Server) Stop() {
	close(s.shutdown)

	// Fechar socket do servidor
	if s.sockfd > 0 {
		syscall.Close(s.sockfd)
		fmt.Printf("🔌 Socket servidor (FD: %d) fechado\n", s.sockfd)
	}

	// Fechar todas as conexões dos clientes
	s.mutex.Lock()
	for conn := range s.clients {
		conn.Write([]byte("🛑 Servidor sendo desligado. Conexão será encerrada.\n"))
		conn.Close()
	}
	s.mutex.Unlock()

	fmt.Println("✅ Servidor encerrado com sucesso!")
}

func main() {
	server := NewServer("8080")

	if err := server.Start(); err != nil {
		log.Fatalf("❌ Erro fatal: %v", err)
	}
}
