package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Client struct {
	serverAddr string
	sockfd     int
	conn       net.Conn
	connected  bool
}

func NewClient(serverAddr string) *Client {
	return &Client{
		serverAddr: serverAddr,
		connected:  false,
	}
}

func (c *Client) Connect() error {
	fmt.Printf("🔄 Conectando ao servidor %s usando socket raw...\n", c.serverAddr)

	// Criar socket TCP
	sockfd, err := syscall.Socket(syscall.AF_INET6, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("❌ Erro ao criar socket: %v", err)
	}
	c.sockfd = sockfd

	// Parsear endereço do servidor
	parts := strings.Split(c.serverAddr, ":")
	if len(parts) != 2 {
		syscall.Close(sockfd)
		return fmt.Errorf("❌ Formato de endereço inválido: %s", c.serverAddr)
	}

	host := parts[0]
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("❌ Porta inválida: %s", parts[1])
	}

	// Resolver endereço IP
	var ip [4]byte
	if host == "localhost" {
		ip = [4]byte{127, 0, 0, 1}
	} else {
		// Resolver IP usando net.ResolveIPAddr para compatibilidade
		addr, err := net.ResolveIPAddr("ip4", host)
		if err != nil {
			syscall.Close(sockfd)
			return fmt.Errorf("❌ Erro ao resolver endereço %s: %v", host, err)
		}
		ipBytes := addr.IP.To4()
		if ipBytes == nil {
			syscall.Close(sockfd)
			return fmt.Errorf("❌ Endereço IPv4 inválido: %s", host)
		}
		copy(ip[:], ipBytes)
	}

	// Configurar endereço de destino
	serverAddr := &syscall.SockaddrInet4{
		Port: port,
		Addr: ip,
	}

	fmt.Printf("🔌 Conectando socket (FD: %d) para %d.%d.%d.%d:%d...\n",
		sockfd, ip[0], ip[1], ip[2], ip[3], port)

	// Conectar usando syscall
	err = syscall.Connect(sockfd, serverAddr)
	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("❌ Erro ao conectar: %v", err)
	}

	// Converter socket para net.Conn para facilitar I/O
	file := os.NewFile(uintptr(sockfd), "tcp-connection")
	c.conn, err = net.FileConn(file)
	file.Close() // Fechar file descriptor, mas manter conexão

	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("❌ Erro ao converter socket para net.Conn: %v", err)
	}

	c.connected = true
	fmt.Printf("✅ Conectado com sucesso ao servidor!\n")
	fmt.Printf("🔗 Conexão estabelecida: %s → %s (Socket FD: %d)\n",
		c.conn.LocalAddr().String(), c.conn.RemoteAddr().String(), sockfd)

	return nil
}

func (c *Client) Disconnect() {
	if c.conn != nil {
		c.conn.Close()
		c.connected = false
		fmt.Printf("🔌 Conexão fechada (Socket FD: %d)\n", c.sockfd)
	}

	if c.sockfd > 0 {
		syscall.Close(c.sockfd)
		fmt.Println("🔌 Socket desconectado do servidor")
	}
}

func (c *Client) sendMessage(message string) error {
	if !c.connected {
		return fmt.Errorf("❌ Cliente não está conectado")
	}

	// Enviar mensagem
	_, err := c.conn.Write([]byte(message + "\n"))
	if err != nil {
		c.connected = false
		return fmt.Errorf("❌ Erro ao enviar mensagem: %v", err)
	}

	return nil
}

func (c *Client) readResponse() (string, error) {
	if !c.connected {
		return "", fmt.Errorf("❌ Cliente não está conectado")
	}

	// Configurar timeout para leitura
	c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(c.conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		c.connected = false
		return "", fmt.Errorf("❌ Erro ao ler resposta: %v", err)
	}

	return strings.TrimSpace(response), nil
}

func (c *Client) Start() {
	// Conectar ao servidor
	if err := c.Connect(); err != nil {
		fmt.Printf("Erro: %v\n", err)
		return
	}
	defer c.Disconnect()

	// Goroutine para ler respostas do servidor
	go func() {
		for c.connected {
			response, err := c.readResponse()
			if err != nil {
				if c.connected {
					fmt.Printf("Erro na leitura: %v\n", err)
				}
				break
			}
			fmt.Printf("📥 Servidor: %s\n", response)
		}
	}()

	// Interface interativa
	c.interactiveMode()
}

func (c *Client) interactiveMode() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("🎮 CLIENTE TCP INTERATIVO")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Println("💡 Digite suas mensagens ou comandos:")
	fmt.Println("   • help - listar comandos disponíveis")
	fmt.Println("   • quit - sair do cliente")
	fmt.Println("   • Ctrl+C - forçar saída")
	fmt.Println(strings.Repeat("=", 50))

	for {
		if !c.connected {
			fmt.Println("❌ Conexão perdida. Encerrando cliente...")
			break
		}

		fmt.Print("💬 Você: ")

		if !scanner.Scan() {
			break
		}

		message := strings.TrimSpace(scanner.Text())

		if message == "" {
			continue
		}

		// Comando local para sair
		if strings.ToLower(message) == "quit" {
			fmt.Println("👋 Encerrando cliente...")
			break
		}

		// Enviar mensagem
		if err := c.sendMessage(message); err != nil {
			fmt.Printf("Erro: %v\n", err)
			break
		}

		// Pequena pausa para dar tempo da resposta chegar
		time.Sleep(100 * time.Millisecond)
	}
}

func (c *Client) TestConnection() {
	fmt.Println("🧪 Iniciando teste de conexão...")

	if err := c.Connect(); err != nil {
		fmt.Printf("Erro no teste: %v\n", err)
		return
	}
	defer c.Disconnect()

	// Testes automatizados
	testMessages := []string{
		"ping",
		"time",
		"status",
		"help",
		"Olá, servidor!",
		"clients",
	}

	fmt.Println("\n🔄 Executando testes automatizados...")

	for i, msg := range testMessages {
		fmt.Printf("\n[Teste %d/%d] Enviando: %s\n", i+1, len(testMessages), msg)

		if err := c.sendMessage(msg); err != nil {
			fmt.Printf("❌ Erro: %v\n", err)
			continue
		}

		response, err := c.readResponse()
		if err != nil {
			fmt.Printf("❌ Erro na resposta: %v\n", err)
			continue
		}

		fmt.Printf("✅ Resposta: %s\n", response)
		time.Sleep(1 * time.Second)
	}

	fmt.Println("\n✅ Testes concluídos!")
}

func showUsage() {
	fmt.Println("📖 USO:")
	fmt.Println("  go run client.go [modo] [servidor:porta]")
	fmt.Println()
	fmt.Println("🎯 MODOS:")
	fmt.Println("  interactive  - Modo interativo (padrão)")
	fmt.Println("  test        - Executa testes automatizados")
	fmt.Println()
	fmt.Println("🌐 EXEMPLOS:")
	fmt.Println("  go run client.go")
	fmt.Println("  go run client.go interactive localhost:8080")
	fmt.Println("  go run client.go test localhost:8080")
}

func main() {
	serverAddr := "localhost:8080"
	mode := "interactive"

	// Processar argumentos
	args := os.Args[1:]

	if len(args) > 0 {
		if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
			showUsage()
			return
		}
		mode = args[0]
	}

	if len(args) > 1 {
		serverAddr = args[1]
	}

	fmt.Printf("🚀 Cliente TCP em Go\n")
	fmt.Printf("📡 Servidor: %s\n", serverAddr)
	fmt.Printf("🎮 Modo: %s\n\n", mode)

	client := NewClient(serverAddr)

	switch mode {
	case "test":
		client.TestConnection()
	case "interactive":
		client.Start()
	default:
		fmt.Printf("❌ Modo inválido: %s\n", mode)
		showUsage()
	}
}
