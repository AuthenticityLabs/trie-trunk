package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"trie-trunk/go/codec"
	logging "trie-trunk/go/logger/client"
	"trie-trunk/go/models"
)

const (
	CustomerService  logging.ServiceId    = 1
	CustomerResource logging.ResourceId   = 1
	LookupCustomer   logging.OperationId  = 1
	DatabaseLookup   logging.CheckpointId = 1
)

func main() {
	dictionary, err := codec.NewDictionary("customers/v1", map[string]map[uint16]string{
		"service": {1: "customers"}, "resource": {1: "customer"}, "operation": {1: "lookup"}, "checkpoint": {1: "database"},
	})
	if err != nil {
		log.Fatal(err)
	}
	logger, err := logging.New(os.Stdout, logging.Config{
		OnError:    func(err error) { log.Printf("log/response write failed: %v", err) },
		Format:     codec.ReadableJSON, // Switch to CBOR for binary production output.
		Dictionary: dictionary, Service: codec.Ref(uint16(CustomerService)), Version: "1.0.0",
	})
	if err != nil {
		log.Fatal(err)
	}
	handler := logger.Handle(func(w http.ResponseWriter, r *http.Request) error {
		entry := logging.FromContext(r.Context()).Resource(CustomerResource).Operation(LookupCustomer)
		id := r.URL.Query().Get("id")
		if id == "" {
			return models.New(models.MISSING_REQUEST_PARAMETER)
		}
		entry.Field("customer_id", codec.String(id))
		start := time.Now()
		// Perform the real lookup here; record its duration without a stop closure.
		entry.Checkpoint(DatabaseLookup, time.Since(start))
		w.Header().Set("Content-Type", "text/plain")
		_, err := fmt.Fprintln(w, "customer found")
		return err
	})
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
