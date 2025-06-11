package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
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
	fmt.Printf("Conectando ao servidor %s usando socket raw...\n", c.serverAddr)

	// Criar socket TCP
	sockfd, err := syscall.Socket(syscall.AF_INET6, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("erro ao criar socket: %v", err)
	}
	c.sockfd = sockfd

	// Parsear endereço do servidor
	parts := strings.Split(c.serverAddr, ":")
	if len(parts) != 2 {
		syscall.Close(sockfd)
		return fmt.Errorf("formato de endereço inválido: %s", c.serverAddr)
	}

	host := parts[0]
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("porta inválida: %s", parts[1])
	}

	// Resolver endereço IP
	var ip [16]byte
	if host == "localhost" {
		ip = [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1} // ::1
	} else {
		// Resolver IP usando net.ResolveIPAddr para IPv6
		addr, err := net.ResolveIPAddr("ip6", host)
		if err != nil {
			syscall.Close(sockfd)
			return fmt.Errorf("erro ao resolver endereço %s: %v", host, err)
		}
		ipBytes := addr.IP.To16()
		if ipBytes == nil {
			syscall.Close(sockfd)
			return fmt.Errorf("endereço ipv6 inválido: %s", host)
		}
		copy(ip[:], ipBytes)
	}

	// Configurar endereço de destino
	serverAddr := &syscall.SockaddrInet6{
		Port: port,
		Addr: ip,
	}

	fmt.Printf("Conectando socket (FD: %d) para %d.%d.%d.%d:%d...\n",
		sockfd, ip[0], ip[1], ip[2], ip[3], port)

	// Conectar usando syscall
	err = syscall.Connect(sockfd, serverAddr)
	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("erro ao conectar: %v", err)
	}

	// Converter socket para net.Conn para facilitar I/O
	file := os.NewFile(uintptr(sockfd), "tcp-connection")
	c.conn, err = net.FileConn(file)
	file.Close() // Fechar file descriptor, mas mantem conexão

	if err != nil {
		syscall.Close(sockfd)
		return fmt.Errorf("erro ao converter socket para net.Conn: %v", err)
	}

	c.connected = true
	fmt.Printf("Conectado com sucesso ao servidor!\n")
	fmt.Printf("Conexão estabelecida: %s → %s (Socket FD: %d)\n",
		c.conn.LocalAddr().String(), c.conn.RemoteAddr().String(), sockfd)

	return nil
}

func (c *Client) Disconnect() {
	if c.conn != nil {
		c.conn.Close()
		c.connected = false
		fmt.Printf("Conexão fechada (Socket FD: %d)\n", c.sockfd)
	}

	if c.sockfd > 0 {
		syscall.Close(c.sockfd)
		fmt.Println("Socket desconectado do servidor")
	}
}

func (c *Client) sendMessage(message string) error {
	if !c.connected {
		return fmt.Errorf("cliente não está conectado")
	}

	// Enviar mensagem
	_, err := c.conn.Write([]byte(message + "\n"))
	if err != nil {
		c.connected = false
		return fmt.Errorf("erro ao enviar mensagem: %v", err)
	}

	return nil
}

// Função para receber arquivo do servidor
func (c *Client) receiveFile(reader *bufio.Reader) error {
	fmt.Println("Iniciando recepção de arquivo...")

	// Ler cabeçalho do arquivo
	var filename string
	var fileSize int64
	var expectedHash string

	// Loop para ler o cabeçalho do arquivo linha por linha
	for {
		line, err := reader.ReadString('\n') // Lê uma linha do cabeçalho
		if err != nil {
			return fmt.Errorf("erro ao ler cabeçalho: %v", err)
		}

		line = strings.TrimSpace(line) // Remove espaços em branco e quebras de linha

		if line == "---CONTENT---" {
			// Encontrou o marcador de início do conteúdo do arquivo, sai do loop do cabeçalho
			break
		}

		// Extrai o nome do arquivo, se a linha começar com "Filename: "
		if strings.HasPrefix(line, "Filename: ") {
			filename = strings.TrimPrefix(line, "Filename: ")
			// Extrai o tamanho do arquivo, se a linha começar com "Size: "
		} else if strings.HasPrefix(line, "Size: ") {
			sizeStr := strings.TrimPrefix(line, "Size: ")
			fileSize, err = strconv.ParseInt(sizeStr, 10, 64)
			if err != nil {
				return fmt.Errorf("erro ao parsear tamanho do arquivo: %v", err)
			}
			// Extrai o hash SHA-256 esperado, se a linha começar com "SHA256: "
		} else if strings.HasPrefix(line, "SHA256: ") {
			expectedHash = strings.TrimPrefix(line, "SHA256: ")
		}
	}

	if filename == "" {
		return fmt.Errorf("nome do arquivo não encontrado no cabeçalho")
	}

	fmt.Printf("Recebendo arquivo: %s (Tamanho: %d bytes)\n", filename, fileSize)

	// Ler conteúdo em base64 até encontrar o footer
	var encodedContent strings.Builder

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("erro ao ler conteúdo: %v", err)
		}

		line = strings.TrimSpace(line)

		if line == "---END_CONTENT---" {
			break
		}

		encodedContent.WriteString(line)
	}

	// Decodificar base64
	fileContent, err := base64.StdEncoding.DecodeString(encodedContent.String())

	//Modifica o fileContent para testar falha na transferência de arquivo
	if filename == "arvore.jpg" {
		fileContent = []byte("Arquivo corrompido para teste")
	}
	if err != nil {
		return fmt.Errorf("erro ao decodificar base64: %v", err)
	}

	// Verificar hash SHA-256
	if expectedHash != "" {
		hash := sha256.Sum256(fileContent)
		actualHash := fmt.Sprintf("%x", hash)

		if actualHash != expectedHash {

			// Se o hash não confere, deletar o arquivo recebido
			os.Remove("received_" + filename)

			// Retornar erro informando o hash esperado e recebido
			return fmt.Errorf("hash SHA-256 não confere! Esperado: %s, Recebido: %s", expectedHash, actualHash)
		}
		fmt.Printf("Hash SHA-256 verificado com sucesso: %s\n", actualHash)
	}

	// Salvar arquivo no disco
	outputFilename := "received_" + filename
	outputFile, err := os.Create(outputFilename)
	if err != nil {
		return fmt.Errorf("erro ao criar arquivo de saída: %v", err)
	}
	defer outputFile.Close()

	_, err = outputFile.Write(fileContent)
	if err != nil {
		return fmt.Errorf("erro ao escrever arquivo: %v", err)
	}

	fmt.Printf("Arquivo recebido e salvo como: %s\n", outputFilename)
	fmt.Printf("Tamanho recebido: %d bytes\n", len(fileContent))

	// Ler linha final "FILE_TRANSFER_END"
	_, err = reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("erro ao ler footer final: %v", err)
	}

	return nil
}

func (c *Client) Start() {
	// Conectar ao servidor
	if err := c.Connect(); err != nil {
		fmt.Printf("Erro: %v\n", err)
		return
	}
	defer c.Disconnect()

	// Criar reader para uso compartilhado
	reader := bufio.NewReader(c.conn)

	// Goroutine para ler respostas do servidor
	go func() {
		for c.connected {
			// Configurar timeout para leitura
			c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))

			response, err := reader.ReadString('\n')
			if err != nil {
				if c.connected {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue // Timeout é normal, continuar
					}
					fmt.Printf("Erro na leitura: %v\n", err)
				}
				break
			}

			response = strings.TrimSpace(response)

			// Verificar se é início de transferência de arquivo
			if response == "FILE_TRANSFER_START" {
				err := c.receiveFile(reader)
				if err != nil {
					fmt.Printf("Erro ao receber arquivo: %v\n", err)
				}
			} else {
				fmt.Printf("Servidor: %s\n", response)
			}
		}
	}()

	// Interface interativa
	c.interactiveMode()
}

func (c *Client) interactiveMode() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("CLIENTE TCP INTERATIVO")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Println("Digite suas mensagens ou comandos:")
	fmt.Println("   • requisicao <arquivo> - fazer requisição de arquivo para o servidor")
	fmt.Println("   • chat <mensagem> - enviar mensagem de chat para outros clientes")
	fmt.Println("   • clients - listar clientes conectados")
	fmt.Println("   • sair - sair do cliente")
	fmt.Println("   • Ctrl+C - forçar saída")
	fmt.Println(strings.Repeat("=", 50))

	for {
		if !c.connected {
			fmt.Println("Conexão perdida. Encerrando cliente...")
			break
		}

		fmt.Print("Você: ")

		if !scanner.Scan() {
			break
		}

		message := strings.TrimSpace(scanner.Text())

		if message == "" {
			continue
		}

		// Comando local para sair
		if strings.ToLower(message) == "sair" {
			fmt.Println("Encerrando cliente...")
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

func main() {
	// Endereço do servidor
	serverAddr := "localhost:8080"

	fmt.Printf("Cliente TCP em Go\n")
	fmt.Printf("Servidor: %s\n", serverAddr)

	client := NewClient(serverAddr)
	client.Start()
}
