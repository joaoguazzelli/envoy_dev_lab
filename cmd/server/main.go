package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	echo "github.com/joaoguazzelli/envoy_dev_lab/pkg/echo_v1"
	echo_messages "github.com/joaoguazzelli/envoy_dev_lab/pkg/echo_v1/messages"
	"gitlab.com/techschool/pcbook/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

var cntServers, defPort int

func seedUsers(userStore UserStore) error {
	err := createUser(userStore, "admin1", "secret", "admin")
	if err != nil {
		return err
	}
	return createUser(userStore, "user1", "secret", "user")
}

func createUser(userStore UserStore, username, password, role string) error {
	user, err := NewUser(username, password, role)
	if err != nil {
		return err
	}
	return userStore.Save(user)
}

const (
	secretKey     = "secret"
	tokenDuration = 15 * time.Minute
)

const (
	serverCertFile   = "cert/server-cert.pem"
	serverKeyFile    = "cert/server-key.pem"
	clientCACertFile = "cert/ca-cert.pem"
)

func loadTLSCredentials() (credentials.TransportCredentials, error) {
	// Load certificate of the CA who signed client's certificate
	pemClientCA, err := ioutil.ReadFile(clientCACertFile)
	if err != nil {
		return nil, err
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(pemClientCA) {
		return nil, fmt.Errorf("failed to add client CA's certificate")
	}

	// Load server's certificate and private key
	serverCert, err := tls.LoadX509KeyPair(serverCertFile, serverKeyFile)
	if err != nil {
		return nil, err
	}

	// Create the credentials and return it
	config := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
	}

	return credentials.NewTLS(config), nil
}

func accessibleRoles() map[string][]string {
	return map[string][]string{
		"/pkg/echo_v1": {"admin"},
	}
}

func runGRPCServer(
	authServer pb.AuthServiceServer,
	jwtManager *JWTManager,
	enableTLS bool,
	listener net.Listener,
) error {
	interceptor := NewAuthInterceptor(jwtManager, accessibleRoles())
	serverOptions := []grpc.ServerOption{
		grpc.UnaryInterceptor(interceptor.Unary()),
		grpc.StreamInterceptor(interceptor.Stream()),
	}

	if enableTLS {
		tlsCredentials, err := loadTLSCredentials()
		if err != nil {
			return fmt.Errorf("cannot load TLS credentials: %w", err)
		}

		serverOptions = append(serverOptions, grpc.Creds(tlsCredentials))
	}

	grpcServer := grpc.NewServer(serverOptions...)

	pb.RegisterAuthServiceServer(grpcServer, authServer)
	reflection.Register(grpcServer)

	log.Printf("Start GRPC server at %s, TLS = %t", listener.Addr().String(), enableTLS)
	return grpcServer.Serve(listener)
}

func init() {
	flag.IntVar(&cntServers, "servers", 1, "how many servers to start")
	flag.IntVar(&defPort, "port", 50051, "default port to start listen server from")
}

type server struct {
	echo.UnimplementedEchoServiceServer
}

func (s server) Echo(_ context.Context, in *echo_messages.EchoRequest) (*echo_messages.EchoResponse, error) {
	log.Printf("---> got RPC call with msg: %s\n", in.GetMsg())
	return &echo_messages.EchoResponse{
		Msg: "echo: " + in.GetMsg(),
	}, nil
}

type healthServer struct{}

func (s healthServer) Check(ctx context.Context, in *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}
func (s healthServer) Watch(in *healthpb.HealthCheckRequest, srv healthpb.Health_WatchServer) error {
	return status.Error(codes.Unimplemented, "Watch is not implemented")
}

func main() {
	//port := flag.Int("port", 0, "the server port")
	enableTLS := flag.Bool("tls", false, "enable SSL/TLS")
	serverType := "grpc"
	flag.Parse()

	userStore := NewInMemoryUserStore()
	err := seedUsers(userStore)
	if err != nil {
		log.Fatal("cannot seed users: ", err)
	}

	jwtManager := NewJWTManager(secretKey, tokenDuration)
	authServer := NewAuthServer(userStore, jwtManager)

	//address := fmt.Sprintf("0.0.0.0:%d", *port)
	address := "0.0.0.0:0"
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatal("cannot start server: ", err)
	}

	if serverType == "grpc" {
		err = runGRPCServer(authServer, jwtManager, *enableTLS, listener)
	}

	if err != nil {
		log.Fatal("cannot start server: ", err)
	}
	// ---------------------------------------------------------------------------------
	flag.Parse()

	if len(flag.Args()) > 2 {
		flag.Usage()
		os.Exit(128)
	}

	if cntServers < 1 {
		cntServers = 1
	}
	if cntServers > 10 {
		cntServers = 10
	}

	var servers []*grpc.Server

	wg := sync.WaitGroup{}
	for i := 0; i < cntServers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()

			port := ":" + strconv.Itoa(defPort+i)
			lis, err := net.Listen("tcp", port)
			if err != nil {
				log.Fatalf("failed listen port %s: %s\n", port, err.Error())
			}

			s := grpc.NewServer()
			echo.RegisterEchoServiceServer(s, &server{})
			healthpb.RegisterHealthServer(s, &healthServer{})
			reflection.Register(s)

			servers = append(servers, s)

			log.Printf("started serving on port %s ...\n", port)
			if err = s.Serve(lis); err != nil {
				log.Fatalf("failed to serve on port %s: %s\n", port, err.Error())
			}
		}()
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	st := <-interrupt
	log.Println("got signal", st.String())

	time.Sleep(time.Millisecond * 200)
	shutdown(servers...)
	time.Sleep(time.Millisecond * 200)

	wg.Wait()
}

func shutdown(servers ...*grpc.Server) {
	if len(servers) == 0 {
		return
	}

	wg := sync.WaitGroup{}
	for _, s := range servers {
		s := s
		if s == nil {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			done := make(chan struct{})
			go func() {
				s.GracefulStop()
				close(done)
			}()

			select {
			case <-done:
				log.Println("gracefully shutdown server ...")
			case <-time.After(time.Second):
				log.Println("timeout for gracefull shutdown, stoping ...")
				s.Stop()
			}
		}()
	}
	wg.Wait()
}
