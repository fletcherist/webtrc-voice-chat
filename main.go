package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/pion/webrtc/v2"

	"net/http"
)

// Prepare the configuration
var peerConnectionConfig = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},
}

func main() {

	handlePing := func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "pong")
	}
	// handleOffer := func(w http.ResponseWriter, r *http.Request) {
	// fmt.Println("server: icoming offer request")
	// w.Header().Add("Access-Control-Allow-Headers", "*")
	// w.Header().Add("Access-Control-Allow-Origin", "*")
	// if r.Method != "POST" {
	// 	http.NotFound(w, r)
	// 	return
	// }
	// // buf := make([]byte, )
	// var offer webrtc.SessionDescription
	// err := json.NewDecoder(r.Body).Decode(&offer)
	// if err != nil {
	// 	http.Error(w, "invalid offer format", 400)
	// 	return
	// }

	// answer, err := room.AddUser(offer)
	// if err != nil {
	// 	http.Error(w, fmt.Sprint("cant accept offer:", err), http.StatusBadRequest)
	// 	return
	// }
	// // json.Marshal(obj)
	// // io.Write(w, `{"ok": true}`)
	// bytes, err := json.Marshal(answer)
	// if err != nil {
	// 	http.Error(w, "server error", http.StatusInternalServerError)
	// 	return
	// }
	// w.WriteHeader(200)
	// w.Write(bytes)
	// return
	// }

	room := newRoom()
	go room.run()

	handleWs := func(w http.ResponseWriter, r *http.Request) {
		serveWs(room, w, r)
	}
	port := os.Getenv("PORT")
	if port == "" {
		// port = "8080"
		port = "80"
		log.Printf("Defaulting to port %s", port)
	}
	addr := fmt.Sprintf(":%s", port)
	fmt.Printf("listening on %s\n", addr)
	http.HandleFunc("/", handlePing)
	// http.HandleFunc("/offer", handleOffer)
	http.HandleFunc("/ws", handleWs)
	log.Fatal(http.ListenAndServe(addr, nil))
}
