package main

import (
	"errors"
)

// Room maintains the set of active clients and broadcasts messages to the
// clients.
type Room struct {
	users     map[*User]bool // Registered clients.
	broadcast chan []byte    // Inbound messages from the clients.
	join      chan *User     // Register requests from the clients.
	leave     chan *User     // Unregister requests from clients.
}

// RoomNew creates new room
func RoomNew() *Room {
	return &Room{
		broadcast: make(chan []byte),
		join:      make(chan *User),
		leave:     make(chan *User),
		users:     make(map[*User]bool),
	}
}

// GetUsers converts map[int64]*User to list
func (r *Room) GetUsers() []*User {
	users := []*User{}
	for user := range r.users {
		users = append(users, user)
	}
	return users
}

// GetOtherUsers returns other users of room except current
func (r *Room) GetOtherUsers(user *User) []*User {
	users := []*User{}
	for userCandidate := range r.users {
		if user.ID == userCandidate.ID {
			continue
		}
		users = append(users, userCandidate)
	}
	return users
}

// Join connects user and room
func (r *Room) Join(user *User) {
	r.join <- user
}

// Leave disconnects user and room
func (r *Room) Leave(user *User) {
	r.leave <- user
}

// GetUsersCount return users count in the room
func (r *Room) GetUsersCount() int {
	return len(r.GetUsers())
}

func (r *Room) run() {
	for {
		select {
		case user := <-r.join:
			r.users[user] = true
		case user := <-r.leave:
			if _, ok := r.users[user]; ok {
				delete(r.users, user)
				close(user.send)
			}
		case message := <-r.broadcast:
			for user := range r.users {
				select {
				case user.send <- message:
				default:
					close(user.send)
					delete(r.users, user)
				}
			}
		}
	}
}

// Rooms is a set of rooms
type Rooms struct {
	rooms map[string]*Room
}

var errNotFound = errors.New("not found")

// Get room by room id
func (r *Rooms) Get(roomID string) (*Room, error) {
	if room, exists := r.rooms[roomID]; exists {
		return room, nil
	}
	return nil, errNotFound
}

// GetOrCreate creates room if it does not exist
func (r *Rooms) GetOrCreate(roomID string) *Room {
	room, err := r.Get(roomID)
	if err == nil {
		return room
	}
	newRoom := RoomNew()
	r.AddRoom(roomID, newRoom)
	go newRoom.run()
	return newRoom

}

// AddRoom adds room to rooms list
func (r *Rooms) AddRoom(roomID string, room *Room) error {
	if _, exists := r.rooms[roomID]; exists {
		return errors.New("room with id " + roomID + " already exists")
	}
	r.rooms[roomID] = room
	return nil
}

// RemoveRoom remove room from rooms list
func (r *Rooms) RemoveRoom(roomID string) error {
	if _, exists := r.rooms[roomID]; exists {
		delete(r.rooms, roomID)
		return nil
	}
	return nil
}

// // Watch for debug
// func (r *Rooms) Watch() {
// 	ticker := time.NewTicker(time.Second * 5)
// 	for range ticker.C {
// 		fmt.Println("rooms watcher:", r.rooms)
// 	}
// }

// RoomsNew creates rooms instance
func RoomsNew() *Rooms {
	return &Rooms{
		rooms: make(map[string]*Room, 100),
	}
}
