package handler

import (
	"bytes"
	"database/sql" // NEW: Required for database operations
	"encoding/json"
	"fmt"
	"log"          // NEW: To log errors for debugging
	"net/http"
	"os"

	_ "github.com/lib/pq" // NEW: The PostgreSQL driver we installed earlier
)

// Structs to parse Telegram's incoming JSON
type Update struct {
	Message *Message `json:"message"`
}
type Message struct {
	Chat *Chat  `json:"chat"`
	Text string `json:"text"`
}
type Chat struct {
	ID int64 `json:"id"`
}

// NEW: Global variable to hold our database connection
var db *sql.DB

// NEW: Function to connect to Supabase securely
func initDB() {
	// If it's already connected (from a previous warm run), do nothing
	if db != nil {
		return
	}

	// Grab the database URL from Vercel's environment variables
	connStr := os.Getenv("DATABASE_URL")
	
	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Printf("Error opening database structure: %v\n", err)
		return
	}

	// Ping the database to confirm the connection is actually alive
	if err = db.Ping(); err != nil {
		log.Printf("Error pinging Supabase: %v\n", err)
	} else {
		log.Println("Successfully connected to Supabase!")
	}
}

// Handler is the serverless entrypoint
func Handler(w http.ResponseWriter, r *http.Request) {
	// NEW: Ensure the database is connected before doing anything else
	initDB()

	// Only accept POST requests from Telegram
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var update Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// If a message was received, process it
	if update.Message != nil {
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

		responseText := "I received your message and checked the database!"
		if update.Message.Text == "/start" {
			responseText = "Welcome to the TKT Prep Bot, powered by Serverless Golang and Supabase! 🚀"
		}

		// Prepare the JSON payload to send back to Telegram
		replyBody, _ := json.Marshal(map[string]interface{}{
			"chat_id": update.Message.Chat.ID,
			"text":    responseText,
		})

		// Send the POST request to Telegram's API
		http.Post(apiURL, "application/json", bytes.NewBuffer(replyBody))
	}

	// Always return a 200 OK to prevent Telegram from retrying the message
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}