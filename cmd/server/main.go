package main

import (
	"KV-Store/api"
	"KV-Store/docs/benchmarks/arena"
	"KV-Store/pkg/metrics"
	pb "KV-Store/proto"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Middleware to record HTTP metrics
func withMetrics(handler http.HandlerFunc, method string, endpoint string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap the writer
		ww := &responseWriterWrapper{ResponseWriter: w, statusCode: 200} // Default to 200

		// Call the original handler
		handler(ww, r)

		// 1. Record Latency
		duration := time.Since(start).Seconds()
		metrics.HttpRequestDuration.WithLabelValues(method, endpoint).Observe(duration)

		// 2. Record Count & Status
		statusStr := fmt.Sprintf("%d", ww.statusCode)
		metrics.HttpRequestsTotal.WithLabelValues(method, endpoint, statusStr).Inc()
	}
}

// Helper struct to capture status code
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

func (ww *responseWriterWrapper) WriteHeader(code int) {
	ww.statusCode = code
	ww.ResponseWriter.WriteHeader(code)
}

// Proxy function
func forwardToLeader(w http.ResponseWriter, r *http.Request, leaderUrl string) {
	req, err := http.NewRequest(r.Method, leaderUrl, r.Body)
	if err != nil {
		http.Error(w, "Failed to create request forwarding", http.StatusInternalServerError)
	}
	client := &http.Client{Timeout: time.Second * 5}
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		http.Error(w, "Failed to create request forwarding", http.StatusInternalServerError)
	}
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		http.Error(w, "Failed to write response forwarding", http.StatusInternalServerError)
	}
}

func main() {
	id := flag.Int("id", 0, "ID of KV store")
	peerAdds := flag.String("peers", "", "Comma separated list of peer addresses")
	rpcPort := flag.String("port", "5001", "gRPC port for Raft communication")
	httpPort := flag.String("http", "8001", "HTTP port for Client communication")
	template := flag.String("peer-template", "http://kv-%d:8001", "Template for peer URLs")

	flag.Parse()

	peerList := strings.Split(*peerAdds, ",")
	var raftClients []pb.RaftServiceClient

	for _, addr := range peerList {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("did not connect to peer %s: %v", addr, err)
		}
		raftClients = append(raftClients, pb.NewRaftServiceClient(conn))
	}

	store, err := benchmarks.NewKVStore(raftClients, *id)
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
	//withMetrics measures latency, traffic & errors using prometheus
	http.HandleFunc("/get", withMetrics(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		val, found := store.Get(key)
		if !found {
			http.Error(w, "Key not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(val))
	}, "GET", "/get"))

	//withMetrics measures latency, traffic & errors
	http.HandleFunc("/put", withMetrics(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		val := r.URL.Query().Get("val")
		err := store.Put(key, val, false)
		if err != nil {
			// Proxy request to leader
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
				leaderUrl := fmt.Sprintf(*template, leaderId)
				targetUrl := fmt.Sprintf("%s/put?key=%s&val=%s", leaderUrl, key, val)
				fmt.Printf("[Forwarding] Redirecting to Leader: %s\n", targetUrl)
				forwardToLeader(w, r, targetUrl)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("Success"))
	}, "GET", "/put"))

	//prometheus metrics
	http.Handle("/metrics", promhttp.Handler())

	fmt.Printf("KV Store HTTP server listening on %s\n", *httpPort)
	// This blocks forever and serves requests
	log.Fatal(http.ListenAndServe(":"+*httpPort, nil))
}
