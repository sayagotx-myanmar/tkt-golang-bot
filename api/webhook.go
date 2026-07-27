package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// ==========================================
// 1. DATA MODELS (STRUCTS)
// ==========================================

type TKTQuestion struct {
	ID            int
	QuestionText  string
	CorrectOption string
	WrongOption1  string
	WrongOption2  string
	Explanation   string
}

type Update struct {
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	Chat *Chat  `json:"chat"`
	Text string `json:"text"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

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
		log.Printf("Error opening database: %v\n", err)
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

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/", botToken)

	// ==========================================
	// SCENARIO A: THE USER CLICKED A BUTTON
	// ==========================================
	if update.CallbackQuery != nil {
		// Tell Telegram we received the click (stops the loading animation)
		http.Post(apiURL+"answerCallbackQuery", "application/json", bytes.NewBuffer([]byte(fmt.Sprintf(`{"callback_query_id": "%s"}`, update.CallbackQuery.ID))))

		dataParts := strings.Split(update.CallbackQuery.Data, ":")
		
		// EVENT 1: User chose a category (or clicked "Next Question")
		if dataParts[0] == "cat" && len(dataParts) == 2 {
			category := dataParts[1]
			
			// Fetch a random question FROM THIS SPECIFIC CATEGORY
			var q TKTQuestion
			err := db.QueryRow(`
				SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2, explanation 
				FROM questions 
				WHERE category = $1 
				ORDER BY RANDOM() 
				LIMIT 1`, category).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2, &q.Explanation)

			var responseText string
			var replyMarkup interface{} = nil

			if err != nil {
				responseText = "No questions found for this category yet!"
			} else {
				responseText = fmt.Sprintf("📚 *Topic: %s*\n\n%s", category, q.QuestionText)

				type Option struct {
					Text string
					Data string
				}
				
				// We attach the category string to the answer data so we can remember it!
				options := []Option{
					{Text: q.CorrectOption, Data: fmt.Sprintf("ans:correct:%d:%s", q.ID, category)},
					{Text: q.WrongOption1, Data: fmt.Sprintf("ans:wrong:%d:%s", q.ID, category)},
					{Text: q.WrongOption2, Data: fmt.Sprintf("ans:wrong:%d:%s", q.ID, category)},
				}

				randSource := rand.NewSource(time.Now().UnixNano())
				rander := rand.New(randSource)
				rander.Shuffle(len(options), func(i, j int) {
					options[i], options[j] = options[j], options[i]
				})

				var keyboard [][]InlineKeyboardButton
				for _, opt := range options {
					keyboard = append(keyboard, []InlineKeyboardButton{{Text: opt.Text, CallbackData: opt.Data}})
				}
				replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
			}

			payload := map[string]interface{}{
				"chat_id":    update.CallbackQuery.Message.Chat.ID,
				"text":       responseText,
				"parse_mode": "Markdown",
			}
			if replyMarkup != nil {
				payload["reply_markup"] = replyMarkup
			}
			
			replyBody, _ := json.Marshal(payload)
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))

		// EVENT 2: User clicked an answer
		} else if dataParts[0] == "ans" && len(dataParts) == 4 {
			status := dataParts[1]
			questionID := dataParts[2]
			category := dataParts[3]

			var explanation string
			db.QueryRow("SELECT explanation FROM questions WHERE id = $1", questionID).Scan(&explanation)

			var responseText string
			if status == "correct" {
				responseText = "✅ *Correct!*\n\n"
			} else {
				responseText = "❌ *Incorrect!*\n\n"
			}
			responseText += "*Explanation:*\n" + explanation

			// Notice we pass the category back into the Next Question button
			nextKeyboard := InlineKeyboardMarkup{
				InlineKeyboard: [][]InlineKeyboardButton{
					{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
					{{Text: "Change Topic 🔄", CallbackData: "cmd:categories"}},
				},
			}

			replyBody, _ := json.Marshal(map[string]interface{}{
				"chat_id":      update.CallbackQuery.Message.Chat.ID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))
			
		// EVENT 3: User clicked "Change Topic"
		} else if dataParts[0] == "cmd" && dataParts[1] == "categories" {
			// Trick the code into thinking the user typed /test to show the menu again
			update.Message = update.CallbackQuery.Message
			update.Message.Text = "/test"
		}
	} 
	
	// ==========================================
	// SCENARIO B: THE USER TYPED A MESSAGE
	// ==========================================
	if update.Message != nil {
		responseText := "I received your message! Type /test to practice."
		var replyMarkup interface{} = nil

		if update.Message.Text == "/start" {
			responseText = "Welcome to the TKT Prep Bot! 🚀\n\nType /test to start practicing."
		} else if update.Message.Text == "/test" {
			
			// Dynamically find all unique categories in your database
			rows, err := db.Query("SELECT DISTINCT category FROM questions WHERE category IS NOT NULL AND category != ''")
			
			if err != nil {
				responseText = "Error fetching categories: " + err.Error()
			} else {
				defer rows.Close()
				responseText = "📂 *Choose a topic to practice:*"
				var keyboard [][]InlineKeyboardButton
				
				for rows.Next() {
					var cat string
					if err := rows.Scan(&cat); err == nil {
						// Create a button for every unique category found
						keyboard = append(keyboard, []InlineKeyboardButton{{Text: cat, CallbackData: "cat:" + cat}})
					}
				}
				
				if len(keyboard) > 0 {
					replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
				} else {
					responseText = "No categories found. Please add some in your Supabase table!"
				}
			}
		}

		payload := map[string]interface{}{
			"chat_id":    update.Message.Chat.ID,
			"text":       responseText,
			"parse_mode": "Markdown",
		}
		if replyMarkup != nil {
			payload["reply_markup"] = replyMarkup
		}

		replyBody, _ := json.Marshal(payload)
		http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}