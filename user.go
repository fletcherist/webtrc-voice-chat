package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v2"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second
	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second
	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10
	// Maximum message size allowed from peer.
	maxMessageSize = 5120
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// User is a middleman between the websocket connection and the hub.
type User struct {
	room  *Room
	conn  *websocket.Conn // The websocket connection.
	send  chan []byte     // Buffered channel of outbound messages.
	Track *webrtc.Track   // WebRTC audio track
}

// readPump pumps messages from the websocket connection to the hub.
func (u *User) readPump() {
	defer func() {
		u.room.Leave(u)
		u.conn.Close()
	}()
	u.conn.SetReadLimit(maxMessageSize)
	u.conn.SetReadDeadline(time.Now().Add(pongWait))
	u.conn.SetPongHandler(func(string) error { u.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, message, err := u.conn.ReadMessage()
		if err != nil {
			fmt.Println(err)
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
				fmt.Println(err)
			}
			break
		}
		message = bytes.TrimSpace(bytes.Replace(message, newline, space, -1))
		go func() {
			err := u.HandleEvent(message)
			if err != nil {
				fmt.Println(err)
				u.SendErr(err)
			}
		}()
	}
}

// Event represents web socket user event
type Event struct {
	Type string `json:"type"`

	Offer  *webrtc.SessionDescription `json:"offer,omitempty"`
	Answer *webrtc.SessionDescription `json:"answer,omitempty"`
	Desc   string                     `json:"desc,omitempty"`
}

// SendJSON sends json body to web socket
func (u *User) SendJSON(body interface{}) error {
	json, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u.send <- json
	return nil
}

// SendErr sends error in json format to web socket
func (u *User) SendErr(err error) error {
	return u.SendJSON(Event{Type: "error", Desc: fmt.Sprint(err)})
}

// HandleEvent handles user event
func (u *User) HandleEvent(eventRaw []byte) error {
	var event *Event
	err := json.Unmarshal(eventRaw, &event)
	if err != nil {
		return err
	}

	fmt.Println("handle event", event.Type)

	if event.Type == "offer" && event.Offer != nil {
		err := u.HandleOffer(*event.Offer)
		if err != nil {
			return err
		}
		return nil
	}

	return u.SendErr(fmt.Errorf("not implemented"))
}

// HandleOffer handles webrtc offer
func (u *User) HandleOffer(offer webrtc.SessionDescription) error {
	mediaEngine := webrtc.MediaEngine{}
	mediaEngine.PopulateFromSDP(offer)

	// Search for Payload type. If the offer doesn't support codec exit since
	// since they won't be able to decode anything we send them
	var payloadType uint8
	for _, audioCodec := range mediaEngine.GetCodecsByKind(webrtc.RTPCodecTypeAudio) {
		fmt.Println(audioCodec.Name)
		if audioCodec.Name == "OPUS" {
			payloadType = audioCodec.PayloadType
			break
		}
	}
	if payloadType == 0 {
		return fmt.Errorf("remote peer does not support opus codec")
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(peerConnectionConfig)
	if err != nil {
		return err
	}
	peerConnection.GetConfiguration()

	// Create Track that we audio back to client on
	userTrack, err := peerConnection.NewTrack(payloadType, rand.Uint32(), "audio", "pion")
	if err != nil {
		return err
	}

	// Add this newly created track to the PeerConnection
	if _, err = peerConnection.AddTrack(userTrack); err != nil {
		return err
	}

	// Set the remote SessionDescription
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		return err
	}

	u.Track = userTrack

	// Set a handler for when a new remote track starts, this handler copies inbound RTP packets,
	// replaces the SSRC and sends them back
	peerConnection.OnTrack(func(remoteTrack *webrtc.Track, receiver *webrtc.RTPReceiver) {
		fmt.Println("peerConnection.OnTrack")
		// Send a PLI on an interval so that the publisher is pushing a keyframe every rtcpPLIInterval
		// This is a temporary fix until we implement incoming RTCP events, then we would push a PLI only when a viewer requests it
		go func() {
			ticker := time.NewTicker(time.Second * 3)
			for range ticker.C {
				errSend := peerConnection.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: remoteTrack.SSRC()}})
				if errSend != nil {
					fmt.Println(errSend)
				}
			}
		}()

		fmt.Printf("Track has started, of type %d: %s \n", remoteTrack.PayloadType(), remoteTrack.Codec().Name)
		for {
			// Read RTP packets being sent to Pion
			rtpPacket, readErr := remoteTrack.ReadRTP()
			if readErr != nil {
				panic(readErr)
			}

			for _, roomUser := range u.room.GetUsers() {
				// dont send rtp packets to owner
				if roomUser == u {
					continue
				}
				if writeErr := roomUser.Track.WriteRTP(rtpPacket); writeErr != nil && writeErr != io.ErrClosedPipe {
					// panic(writeErr)
					fmt.Println("error writing rtp packet", writeErr)
				}
			}
		}
	})

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())
		if connectionState == webrtc.ICEConnectionStateConnected {
			fmt.Println("user joined")
			// room.MembersCount++
			fmt.Println("now members count is", len(u.room.GetUsers()))
		} else if connectionState == webrtc.ICEConnectionStateDisconnected ||
			connectionState == webrtc.ICEConnectionStateFailed ||
			connectionState == webrtc.ICEConnectionStateClosed {
			fmt.Println("user leaved")
			// delete(r.Users, user.ID)
			fmt.Println("now members count is", len(u.room.GetUsers()))
		}
	})
	// Create an answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}
	// Sets the LocalDescription, and starts our UDP listeners
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		panic(err)
	}

	err = u.SendJSON(Event{
		Type:   "answer",
		Answer: &answer,
	})
	return nil
}

// writePump pumps messages from the hub to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (u *User) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		u.conn.Close()
	}()
	for {
		select {
		case message, ok := <-u.send:
			u.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				u.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := u.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued messages to the current websocket message.
			n := len(u.send)
			for i := 0; i < n; i++ {
				w.Write(newline)
				w.Write(<-u.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			u.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := u.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// serveWs handles websocket requests from the peer.
func serveWs(w http.ResponseWriter, r *http.Request) {
	room := newRoom()
	go room.run()

	roomID := strings.ReplaceAll(r.URL.Path, "/", "")
	fmt.Println("ws connection to room:", roomID)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	user := &User{room: room, conn: conn, send: make(chan []byte, 256)}
	user.room.Join(user)

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go user.writePump()
	go user.readPump()
}
