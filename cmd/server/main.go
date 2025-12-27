package main

import (
	"KV-Store/api"
	"KV-Store/kv"
	pb "KV-Store/proto"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const PORT = ":8080"

func main() {
	id := flag.Int("id", 0, "ID of KV store")
	peerAddrs := flag.String("peers", "", "Comma separated list of peer addresses")
	rpcPort := flag.String("port", "5001", "gRPC port for Raft communication")
	httpPort := flag.String("http", "8001", "HTTP port for Client communication")
	// Take template url during production as parameter for request forwarding
	// template := flag.String("url-template", "http://localhost:800%d", "Template for peer URLs")
	template := "http://localhost:%d"
	flag.Parse()

	peerList := strings.Split(*peerAddrs, ",")
	var raftClients []pb.RaftServiceClient

	for _, addr := range peerList {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("did not connect to peer %s: %v", addr, err)
		}
		raftClients = append(raftClients, pb.NewRaftServiceClient(conn))
	}

	store, err := kv.NewKVStore(raftClients, *id)
	if err != nil {
		log.Fatalf("Error intializing KV-Store: %v", err)
	}

	//gRPC server
	lis, err := net.Listen("tcp", ":"+*rpcPort)
	if err != nil {
		log.Fatalf("failed to initialize gRPC server: %v", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterRaftServiceServer(grpcServer, api.NewRaftServer(store.Raft))

	go func() {
		fmt.Printf("Raft gRPC server listening on %s\n", *rpcPort)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	http.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		val, found := store.Get(key) // You need to implement Get in store.go wrapping Raft if needed, or local read
		if !found {
			http.Error(w, "Key not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(val))
	})

	// POST /put?key=foo&val=bar
	http.HandleFunc("/put", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		val := r.URL.Query().Get("val")
		err := store.Put(key, val, false)
		if err != nil {
			if err.Error() == "not leader" {
				leaderId := store.Raft.GetLeader()
				if leaderId == -1 {
					http.Error(w, "Leader not found", http.StatusNotFound)
				}
				//cluster error check
				if leaderId == *id {
					http.Error(w, "Cluster in leadership transition", http.StatusServiceUnavailable)
					return
				}
				leaderUrl := fmt.Sprintf(template, 8001+leaderId)
				fmt.Println("leaderURl: ", leaderUrl)
				target := fmt.Sprintf("%s/put?key=%s&val=%s", leaderUrl, key, val)
				http.Redirect(w, r, target, http.StatusTemporaryRedirect)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("Success"))
	})

	fmt.Printf("KV Store HTTP server listening on %s\n", *httpPort)
	// This blocks forever and serves requests
	log.Fatal(http.ListenAndServe(":"+*httpPort, nil))
}
