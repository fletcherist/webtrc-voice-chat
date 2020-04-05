package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	maxMessageSize = 51200
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
	room           *Room
	conn           *websocket.Conn          // The websocket connection.
	send           chan []byte              // Buffered channel of outbound messages.
	PeerConnection *webrtc.PeerConnection   // WebRTC Peer Connection
	Tracks         map[uint32]*webrtc.Track // WebRTC incoming audio tracks
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
	} else if event.Type == "answer" && event.Answer != nil {
		if u.PeerConnection == nil {
			return errors.New("user has no peer connection")
		}
		u.PeerConnection.SetRemoteDescription(*event.Answer)
		return nil
	}

	return u.SendErr(fmt.Errorf("not implemented"))
}

// GetRoomTracks returns list of room incoming tracks
func (u *User) GetRoomTracks() []*webrtc.Track {
	tracks := []*webrtc.Track{}
	for _, user := range u.room.GetUsers() {
		for _, track := range user.Tracks {
			tracks = append(tracks, track)
		}
	}
	return tracks
}

// SendOffer to the user when he/she connects
func (u *User) SendOffer() error {
	// fmt.Println("123 Add remote track as peerConnection local track")

	// if len(u.Tracks) == 0 {
	// 	track, err := u.PeerConnection.NewTrack(webrtc.DefaultPayloadTypeOpus, rand.Uint32(), "audio", "pion")
	// 	if err != nil {
	// 		panic(err)
	// 	}
	// 	if _, err = u.PeerConnection.AddTrack(track); err != nil {
	// 		fmt.Println("ERROR Add remote track as peerConnection local track", err)
	// 		panic(err)
	// 	}
	// }

	for _, track := range u.GetRoomTracks() {
		if _, err := u.PeerConnection.AddTrack(track); err != nil {
			fmt.Println("ERROR Add remote track as peerConnection local track", err)
			panic(err)
		}
	}

	offer, err := u.PeerConnection.CreateOffer(nil)
	if err != nil {
		panic(err)
	}
	if err = u.PeerConnection.SetLocalDescription(offer); err != nil {
		panic(err)
	}
	if err = u.SendJSON(Event{
		Type:  "offer",
		Offer: &offer,
	}); err != nil {
		panic(err)
	}
	return nil
}

// HandleOffer handles webrtc offer
func (u *User) HandleOffer(offer webrtc.SessionDescription) error {
	mediaEngine := webrtc.MediaEngine{}
	mediaEngine.PopulateFromSDP(offer)

	// Search for Payload type. If the offer doesn't support codec exit since
	// since they won't be able to decode anything we send them
	var payloadType uint8
	for _, audioCodec := range mediaEngine.GetCodecsByKind(webrtc.RTPCodecTypeAudio) {
		if audioCodec.Name == "OPUS" {
			payloadType = audioCodec.PayloadType
			break
		}
	}
	if payloadType == 0 {
		return fmt.Errorf("remote peer does not support opus codec")
	}

	track, err := u.PeerConnection.NewTrack(webrtc.DefaultPayloadTypeOpus, rand.Uint32(), "audio", "pion")
	if err != nil {
		panic(err)
	}
	if _, err = u.PeerConnection.AddTrack(track); err != nil {
		fmt.Println("ERROR Add remote track as peerConnection local track", err)
		panic(err)
	}

	// Set the remote SessionDescription
	if err := u.PeerConnection.SetRemoteDescription(offer); err != nil {
		return err
	}
	answer, err := u.PeerConnection.CreateAnswer(nil)
	if err != nil {
		return err
	}
	// Sets the LocalDescription, and starts our UDP listeners
	if err = u.PeerConnection.SetLocalDescription(answer); err != nil {
		return err
	}
	if err = u.SendJSON(Event{
		Type:   "answer",
		Answer: &answer,
	}); err != nil {
		return err
	}
	return nil
}

// AddTrack adds track dynamically with renegotiation
func (u *User) AddTrack(track *webrtc.Track) error {
	if _, err := u.PeerConnection.AddTrack(track); err != nil {
		fmt.Println("ERROR Add remote track as peerConnection local track", err)
		return err
	}
	offer, err := u.PeerConnection.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err = u.PeerConnection.SetLocalDescription(offer); err != nil {
		return err
	}
	if err = u.SendJSON(Event{
		Type:  "offer",
		Offer: &offer,
	}); err != nil {
		return err
	}
	return nil
}

// serveWs handles websocket requests from the peer.
func serveWs(rooms *Rooms, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	mediaEngine := webrtc.MediaEngine{}
	mediaEngine.RegisterDefaultCodecs()

	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))
	peerConnection, err := api.NewPeerConnection(peerConnectionConfig)

	roomID := strings.ReplaceAll(r.URL.Path, "/", "")
	room := rooms.GetOrCreate(roomID)

	fmt.Println("ws connection to room:", roomID, len(room.GetUsers()), "users")

	user := &User{
		room:           room,
		conn:           conn,
		send:           make(chan []byte, 256),
		PeerConnection: peerConnection,
		Tracks:         make(map[uint32]*webrtc.Track, 2),
	}

	user.PeerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())
		if connectionState == webrtc.ICEConnectionStateConnected {
			fmt.Println("user joined")
			// room.MembersCount++
			fmt.Println("now members count is", len(user.room.GetUsers()))
		} else if connectionState == webrtc.ICEConnectionStateDisconnected ||
			connectionState == webrtc.ICEConnectionStateFailed ||
			connectionState == webrtc.ICEConnectionStateClosed {
			fmt.Println("user leaved")
			// delete(r.Users, user.ID)
			fmt.Println("now members count is", len(user.room.GetUsers()))
		}
	})
	user.PeerConnection.OnTrack(func(remoteTrack *webrtc.Track, receiver *webrtc.RTPReceiver) {
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

		track, err := user.PeerConnection.NewTrack(webrtc.DefaultPayloadTypeOpus, remoteTrack.SSRC(), "audio", "pion")
		if err != nil {
			panic(err)
		}

		user.Tracks[track.SSRC()] = track
		for _, roomUser := range room.GetOtherUsers(user) {
			if err := roomUser.AddTrack(track); err != nil {
				panic(err)
			}
		}

		fmt.Printf("Track has started, of type %d: %s \n", remoteTrack.PayloadType(), remoteTrack.Codec().Name)
		for {
			// Read RTP packets being sent to Pion
			rtpPacket, readErr := remoteTrack.ReadRTP()
			if readErr != nil {
				panic(readErr)
			}
			if writeErr := track.WriteRTP(rtpPacket); writeErr != nil && writeErr != io.ErrClosedPipe {
				fmt.Println("error writing rtp packet", writeErr)
				panic(writeErr)
			}
		}
	})

	user.room.Join(user)

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go user.writePump()
	go user.readPump()

	// user.SendOffer()
}
