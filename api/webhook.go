package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	_ "github.com/lib/pq"
)

// ==========================================
// 1. DATA MODELS (STRUCTS)
// ==========================================

// Struct for your Supabase Database
type TKTQuestion struct {
	ID            int
	QuestionText  string
	CorrectOption string
	WrongOption1  string
	WrongOption2  string
	Explanation   string
}

// Structs for Telegram JSON Payload
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

// Structs for Telegram Buttons
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// ==========================================
// 2. DATABASE CONNECTION LOGIC
// ==========================================

var db *sql.DB

func initDB() {
	if db != nil {
		return
	}
	connStr := os.Getenv("DATABASE_URL")
	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Printf("Error opening database structure: %v\n", err)
		return
	}
	if err = db.Ping(); err != nil {
		log.Printf("Error pinging Supabase: %v\n", err)
	} else {
		log.Println("Successfully connected to Supabase!")
	}
}

// ==========================================
// 3. MAIN WEBHOOK HANDLER
// ==========================================

func Handler(w http.ResponseWriter, r *http.Request) {
	initDB()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var update Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	if update.Message != nil {
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

		// Default response
		responseText := "I received your message and checked the database!"

		// Handle specific commands
		if update.Message.Text == "/start" {
			responseText = "Welcome to the TKT Prep Bot, powered by Serverless Golang and Supabase! 🚀"
		} else if update.Message.Text == "/test" {
			
            // FETCH QUESTION FROM DATABASE HERE
			var q TKTQuestion
			err := db.QueryRow(`
				SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2, explanation 
				FROM questions 
				WHERE id = $1`, 1).Scan(
				&q.ID,
				&q.QuestionText,
				&q.CorrectOption,
				&q.WrongOption1,
				&q.WrongOption2,
				&q.Explanation,
			)

			if err != nil {
				responseText = "Database error: " + err.Error()
			} else {
				// Format the question as a simple text response to prove it works
				responseText = fmt.Sprintf("📚 *Question 1:*\n%s\n\n✅ *Correct Answer:* %s", q.QuestionText, q.CorrectOption)
			}
		}

		replyBody, _ := json.Marshal(map[string]interface{}{
			"chat_id":    update.Message.Chat.ID,
			"text":       responseText,
			"parse_mode": "Markdown", // Allows us to use bold text in Telegram
		})

		http.Post(apiURL, "application/json", bytes.NewBuffer(replyBody))
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}