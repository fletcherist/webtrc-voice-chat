package main

// Room maintains the set of active clients and broadcasts messages to the
// clients.
type Room struct {
	// Registered clients.
	users map[*User]bool
	// Inbound messages from the clients.
	broadcast chan []byte
	// Register requests from the clients.
	join chan *User
	// Unregister requests from clients.
	leave chan *User
}

func newRoom() *Room {
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

func (r *Room) Join(user *User) {
	r.join <- user
}

func (r *Room) Leave(user *User) {
	r.leave <- user
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
