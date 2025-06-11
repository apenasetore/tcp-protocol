package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
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

	addr := syscall.SockaddrInet6{
		Port: port,
		Addr: [16]byte{
			0, 0, 0, 0, // IPv6 address (can be set to 0 for any address)
			0, 0, 0, 0, // IPv6 address (can be set to 0 for any address)
			0, 0, 0, 0, // IPv6 address (can be set to 0 for any address)
			0, 0, 0, 0, // IPv6 address (can be set to 0 for any address)
		},
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
	// Go routine é usado para thread de aceitação de conexões
	// Isso permite que o servidor aceite conexões enquanto processa clientes sem bloquear a thread principal
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
				log.Printf("Erro ao aceitar conexão: %v", err)
				continue
			}
		}

		// Converter socket descriptor para net.Conn
		file := os.NewFile(uintptr(clientFd), "tcp-client")
		conn, err := net.FileConn(file)
		file.Close() // Fechar o file descriptor, mas manter a conexão

		if err != nil {
			log.Printf("Erro ao converter socket para net.Conn: %v", err)
			syscall.Close(clientFd)
			continue
		}

		// Obter endereço do cliente
		var clientAddrStr string
		if addr6, ok := clientAddr.(*syscall.SockaddrInet6); ok {
			clientAddrStr = fmt.Sprintf("%d.%d.%d.%d:%d",
				addr6.Addr[0], addr6.Addr[1], addr6.Addr[2], addr6.Addr[3], addr6.Port)
		} else {
			clientAddrStr = "unknown"
		}

		// Adicionar cliente à lista
		s.mutex.Lock()
		s.clients[conn] = true
		clientCount := len(s.clients)
		s.mutex.Unlock()

		fmt.Printf("Nova conexão estabelecida: %s (FD: %d, Total: %d clientes)\n",
			clientAddrStr, clientFd, clientCount)

		// Tratar cliente em goroutine separada para evitar bloqueio
		// Isso permite que o servidor continue aceitando novas conexões enquanto processa clientes existentes, como uma thread de trabalho
		go s.handleClient(conn)
	}
}

func (s *Server) handleClient(conn net.Conn) {

	// Garantir que o cliente seja removido ao final
	defer func() {
		s.removeClient(conn)
		conn.Close()
	}()

	// Configurar timeout para leitura
	conn.SetReadDeadline(time.Now().Add(5 * time.Minute))

	reader := bufio.NewReader(conn)
	clientAddr := conn.RemoteAddr().String()

	// Enviar mensagem de boas-vindas
	welcome := fmt.Sprintf("Bem-vindo ao servidor TCP! Você está conectado como %s\n", clientAddr)
	conn.Write([]byte(welcome))

	for {
		// Ler mensagem do cliente
		message, err := reader.ReadString('\n')
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				fmt.Printf("Timeout na conexão com %s\n", clientAddr)
			} else {
				fmt.Printf("Cliente %s desconectou\n", clientAddr)
			}
			break
		}

		message = strings.TrimSpace(message)
		fmt.Printf("Message: [%s]: %s\n", clientAddr, message)

		// Processar mensagem
		response := s.processMessage(message, clientAddr, conn)

		// Enviar resposta apenas se não for uma transferência de arquivo
		if !strings.HasPrefix(strings.ToLower(message), "requisicao") {
			_, err = conn.Write([]byte(response + "\n"))
			if err != nil {
				fmt.Printf("Erro ao enviar resposta para %s: %v\n", clientAddr, err)
				break
			}
		}

		// Resetar timeout
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	}
}

func chat(message string, conn net.Conn, s *Server) string {
	parts := strings.Fields(message)
	if len(parts) < 2 || parts[0] != "chat" {
		return "Comando inválido. Use: Chat <mensagem>"
	}
	// Extrair a mensagem do chat
	chatMessage := strings.Join(parts[1:], " ")
	// Enviar mensagem para todos os clientes conectados
	s.mutex.RLock()
	var clientList []string
	for client := range s.clients {
		if client == conn {
			continue // Não enviar para o próprio cliente
		}
		_, err := client.Write([]byte(fmt.Sprintf("Chat de %s: %s\n", conn.RemoteAddr().String(), chatMessage)))
		if err != nil {
			fmt.Printf("Erro ao enviar mensagem de chat para %s: %v\n", client.RemoteAddr().String(), err)
			continue // Ignorar erro e continuar enviando para outros clientes
		}
		clientList = append(clientList, client.RemoteAddr().String())
	}
	s.mutex.RUnlock()
	return fmt.Sprintf("Mensagem enviadas para os clientes (%d): %s", len(clientList), strings.Join(clientList, ", "))

}

func sendFile(message string, conn net.Conn) string {
	// Espera-se que a mensagem seja: "requisicao <nome_do_arquivo>"
	parts := strings.Fields(message)
	if len(parts) < 2 {
		response := "FILE_TRANSFER_ERROR\nErro: Uso: requisicao <nome_do_arquivo>\nFILE_TRANSFER_END\n"
		conn.Write([]byte(response))
		return "Comando inválido enviado"
	}
	filename := parts[1]

	// Verificar se o arquivo existe
	file, err := os.Open(filename)
	if err != nil {
		response := fmt.Sprintf("FILE_TRANSFER_ERROR\nErro: %v\nFILE_TRANSFER_END\n", err)
		conn.Write([]byte(response))
		return fmt.Sprintf("Erro ao abrir arquivo: %v", err)
	}
	defer file.Close()

	// Obter informações do arquivo
	info, err := file.Stat()
	if err != nil {
		response := fmt.Sprintf("FILE_TRANSFER_ERROR\nErro ao obter informações do arquivo: %v\nFILE_TRANSFER_END\n", err)
		conn.Write([]byte(response))
		return fmt.Sprintf("Erro ao obter informações do arquivo: %v", err)
	}

	// Verificar se é um diretório
	if info.IsDir() {
		response := "FILE_TRANSFER_ERROR\nO nome fornecido é um diretório, não um arquivo.\nFILE_TRANSFER_END\n"
		conn.Write([]byte(response))
		return "Diretório fornecido em vez de arquivo"
	}

	// Ler o conteúdo do arquivo
	fileContent, err := io.ReadAll(file)
	if err != nil {
		response := fmt.Sprintf("FILE_TRANSFER_ERROR\nErro ao ler arquivo: %v\nFILE_TRANSFER_END\n", err)
		conn.Write([]byte(response))
		return fmt.Sprintf("Erro ao ler arquivo: %v", err)
	}

	// Calcular SHA-256 do arquivo
	hash := sha256.Sum256(fileContent)
	hashStr := fmt.Sprintf("%x", hash)

	// Codificar arquivo em base64 para transmissão
	encodedContent := base64.StdEncoding.EncodeToString(fileContent)

	fmt.Printf("Iniciando transferência do arquivo '%s' para %s\n", filename, conn.RemoteAddr().String())
	fmt.Printf("Tamanho: %d bytes, SHA-256: %s\n", len(fileContent), hashStr)

	// Enviar sinal de início de transferência
	_, err = conn.Write([]byte("FILE_TRANSFER_START\n"))
	if err != nil {
		return fmt.Sprintf("Erro ao enviar sinal de início: %v", err)
	}

	// Enviar cabeçalho com informações do arquivo
	header := fmt.Sprintf("Filename: %s\nSize: %d\nSHA256: %s\nContent-Encoding: base64\n---CONTENT---\n",
		filename, len(fileContent), hashStr)

	// Enviar cabeçalho
	_, err = conn.Write([]byte(header))
	if err != nil {
		return fmt.Sprintf("Erro ao enviar cabeçalho do arquivo: %v", err)
	}

	// Enviar conteúdo codificado em chunks para arquivos grandes
	chunkSize := 4096
	for i := 0; i < len(encodedContent); i += chunkSize {
		end := i + chunkSize
		if end > len(encodedContent) {
			end = len(encodedContent)
		}

		_, err = conn.Write([]byte(encodedContent[i:end]))
		if err != nil {
			return fmt.Sprintf("Erro ao enviar conteúdo do arquivo: %v", err)
		}

		// Pequena pausa para evitar sobrecarga
		time.Sleep(1 * time.Millisecond)
	}

	// Enviar footer
	footer := "\n---END_CONTENT---\nFILE_TRANSFER_END\n"
	_, err = conn.Write([]byte(footer))
	if err != nil {
		return fmt.Sprintf("Erro ao enviar footer do arquivo: %v", err)
	}

	fmt.Printf("Fim da transferencia do arquivo '%s'  %s\n", filename, conn.RemoteAddr().String())
	return fmt.Sprintf("Fim da trasnferencia do arquivo '%s' com SHA-256: %s", filename, hashStr)
}

func (s *Server) processMessage(message, clientAddr string, conn net.Conn) string {
	command := strings.TrimSpace(strings.ToLower(message))
	if command == "" {
		return "Comando vazio. Por favor, envie uma mensagem válida."
	}

	// Processar comando recebido
	fmt.Printf("Comando recebido de %s: %s\n", clientAddr, command)

	// Verificar se é um comando de requisição de arquivo
	if strings.HasPrefix(command, "requisicao") {
		fmt.Printf("Requisição de arquivo recebida: %s\n", message)
		return sendFile(message, conn)
	}

	// Verificar se é um comando de chat
	if strings.HasPrefix(command, "chat") {
		fmt.Printf("Chat recebido de %s: %s\n", clientAddr, command)
		return chat(message, conn, s)
	}

	// Outros comandos
	switch command {
	case "clients":
		s.mutex.RLock()
		var clientList []string
		for client := range s.clients {
			clientList = append(clientList, client.RemoteAddr().String())
		}
		s.mutex.RUnlock()
		return fmt.Sprintf("Clientes conectados (%d): %s", len(clientList), strings.Join(clientList, ", "))

	case "sair":
		return fmt.Sprintf("Até logo, %s! Desconectando...", clientAddr)

	case "help", "ajuda":
		return "Comandos disponíveis:\n" +
			"  • clients - listar clientes conectados\n" +
			"  • chat <mensagem> - enviar mensagem de chat para todos os clientes\n" +
			"  • requisicao <arquivo> - solicitar arquivo do servidor\n" +
			"  • sair - desconectar do servidor\n" +
			"  • help/ajuda - mostrar esta mensagem"

	default:
		return fmt.Sprintf("Eco: %s", message)
	}
}

func (s *Server) removeClient(conn net.Conn) {
	s.mutex.Lock()
	delete(s.clients, conn)
	clientCount := len(s.clients)
	s.mutex.Unlock()

	fmt.Printf("Cliente desconectado: %s (Restam: %d clientes)\n",
		conn.RemoteAddr().String(), clientCount)
}

func (s *Server) waitForShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	fmt.Println("\nRecebido sinal de shutdown...")
	s.Stop()
}

func (s *Server) Stop() {
	close(s.shutdown)

	// Fechar socket do servidor
	if s.sockfd > 0 {
		syscall.Close(s.sockfd)
		fmt.Printf("Socket servidor (FD: %d) fechado\n", s.sockfd)
	}

	// Fechar todas as conexões dos clientes
	s.mutex.Lock()
	for conn := range s.clients {
		conn.Write([]byte("Servidor sendo desligado. Conexão será encerrada.\n"))
		conn.Close()
	}
	s.mutex.Unlock()

	fmt.Println("Servidor encerrado com sucesso!")
}

func main() {
	server := NewServer("8080")

	if err := server.Start(); err != nil {
		log.Fatalf("Erro fatal: %v", err)
	}
}
