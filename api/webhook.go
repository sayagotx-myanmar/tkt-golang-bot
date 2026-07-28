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
	Chat    *Chat    `json:"chat"`
	Text    string   `json:"text"`
	Contact *Contact `json:"contact"` // Added for phone number sharing
}

type Chat struct {
	ID int64 `json:"id"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	UserID      int64  `json:"user_id"`
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

// Added structs for requesting contact info natively via Telegram
type ReplyKeyboardMarkup struct {
	Keyboard        [][]KeyboardButton `json:"keyboard"`
	ResizeKeyboard  bool               `json:"resize_keyboard"`
	OneTimeKeyboard bool               `json:"one_time_keyboard"`
}

type KeyboardButton struct {
	Text           string `json:"text"`
	RequestContact bool   `json:"request_contact"`
}

type ReplyKeyboardRemove struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
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

// Helper function to check if a user is authorized
func checkAuth(userID int64) bool {
	var id int
	err := db.QueryRow("SELECT id FROM authorized_users WHERE telegram_user_id = $1", userID).Scan(&id)
	return err == nil
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
	// SCENARIO A: THE USER CLICKED AN INLINE BUTTON
	// ==========================================
	if update.CallbackQuery != nil {
		http.Post(apiURL+"answerCallbackQuery", "application/json", bytes.NewBuffer([]byte(fmt.Sprintf(`{"callback_query_id": "%s"}`, update.CallbackQuery.ID))))

		userID := update.CallbackQuery.Message.Chat.ID

		// SECURITY CHECK: Block unauthorized users from clicking buttons
		if !checkAuth(userID) {
			replyBody, _ := json.Marshal(map[string]interface{}{
				"chat_id": userID,
				"text":    "❌ You must be authorized to use this bot. Type /start to verify your account.",
			})
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))
			w.WriteHeader(http.StatusOK)
			return
		}

		dataParts := strings.Split(update.CallbackQuery.Data, ":")
		
		// EVENT 1: User chose a category (or clicked "Next Question")
		if dataParts[0] == "cat" && len(dataParts) == 2 {
			category := dataParts[1]
			
			var q TKTQuestion
			err := db.QueryRow(`
				SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2, explanation 
				FROM questions 
				WHERE category = $1 
				AND id NOT IN (SELECT question_id FROM answered_questions WHERE telegram_user_id = $2)
				ORDER BY RANDOM() 
				LIMIT 1`, category, userID).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2, &q.Explanation)

			var responseText string
			var replyMarkup interface{} = nil

			if err == sql.ErrNoRows {
				responseText = fmt.Sprintf("🎉 *Congratulations!*\n\nYou have completed all the practice questions for *%s*.\n\nType /progress to see your final score, or reset the module to try again.", category)
			} else if err != nil {
				responseText = "Error fetching question. Please try again."
			} else {
				type Option struct {
					Text   string
					Status string
				}
				
				options := []Option{
					{Text: q.CorrectOption, Status: "correct"},
					{Text: q.WrongOption1, Status: "wrong"},
					{Text: q.WrongOption2, Status: "wrong"},
				}

				randSource := rand.NewSource(time.Now().UnixNano())
				rander := rand.New(randSource)
				rander.Shuffle(len(options), func(i, j int) {
					options[i], options[j] = options[j], options[i]
				})

				responseText = fmt.Sprintf("📚 *Topic: %s*\n\n%s\n\n*A)* %s\n*B)* %s\n*C)* %s", 
					category, q.QuestionText, options[0].Text, options[1].Text, options[2].Text)

				keyboard := [][]InlineKeyboardButton{
					{
						{Text: "A", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[0].Status, q.ID, category)},
						{Text: "B", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[1].Status, q.ID, category)},
						{Text: "C", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[2].Status, q.ID, category)},
					},
				}
				replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
			}

			payload := map[string]interface{}{
				"chat_id":    userID,
				"text":       responseText,
				"parse_mode": "Markdown",
			}
			if replyMarkup != nil {
				payload["reply_markup"] = replyMarkup
			}
			
			replyBody, _ := json.Marshal(payload)
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))

		// EVENT 2: User clicked a FIRST-TRY answer
		} else if dataParts[0] == "ans" && len(dataParts) == 4 {
			status := dataParts[1]
			questionID := dataParts[2]
			category := dataParts[3]

			pointsToAdd := 0
			if status == "correct" {
				pointsToAdd = 1
			}
			db.Exec(`
				INSERT INTO user_progress (telegram_user_id, category, points, attempts)
				VALUES ($1, $2, $3, 1)
				ON CONFLICT (telegram_user_id, category) 
				DO UPDATE SET 
					points = user_progress.points + EXCLUDED.points,
					attempts = user_progress.attempts + EXCLUDED.attempts`,
				userID, category, pointsToAdd)

			db.Exec(`
				INSERT INTO answered_questions (telegram_user_id, question_id) 
				VALUES ($1, $2) 
				ON CONFLICT DO NOTHING`, 
				userID, questionID)

			var explanation string
			db.QueryRow("SELECT explanation FROM questions WHERE id = $1", questionID).Scan(&explanation)

			var responseText string
			var nextKeyboard InlineKeyboardMarkup

			if status == "correct" {
				responseText = "✅ *Correct!*\n\n*💡 Hint:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
						{{Text: "Practice different module 🔄", CallbackData: "cmd:categories"}},
					},
				}
			} else {
				responseText = "❌ *Incorrect!*\n\n*💡 Hint:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Try Again 🔄", CallbackData: fmt.Sprintf("retry:%s:%s", questionID, category)}},
						{{Text: "Practice different module 🔄", CallbackData: "cmd:categories"}},
					},
				}
			}

			replyBody, _ := json.Marshal(map[string]interface{}{
				"chat_id":      userID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))

		// EVENT 3: User clicked an answer AFTER hitting Try Again
		} else if dataParts[0] == "retryans" && len(dataParts) == 4 {
			status := dataParts[1]
			questionID := dataParts[2]
			category := dataParts[3]

			var explanation string
			db.QueryRow("SELECT explanation FROM questions WHERE id = $1", questionID).Scan(&explanation)

			var responseText string
			var nextKeyboard InlineKeyboardMarkup

			if status == "correct" {
				responseText = "✅ *Correct!*\n\n*Explanation:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
						{{Text: "Practice different module 🔄", CallbackData: "cmd:categories"}},
					},
				}
			} else {
				responseText = "❌ *Incorrect!*\n\n*💡 Hint:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Try Again 🔄", CallbackData: fmt.Sprintf("retry:%s:%s", questionID, category)}},
						{{Text: "Practice different module 🔄", CallbackData: "cmd:categories"}},
					},
				}
			}

			replyBody, _ := json.Marshal(map[string]interface{}{
				"chat_id":      userID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))
			
		// EVENT 4: User clicked "Try Again"
		} else if dataParts[0] == "retry" && len(dataParts) == 3 {
			questionID := dataParts[1]
			category := dataParts[2]

			var q TKTQuestion
			err := db.QueryRow(`
				SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2, explanation 
				FROM questions 
				WHERE id = $1`, questionID).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2, &q.Explanation)

			var responseText string
			var replyMarkup interface{} = nil

			if err != nil {
				responseText = "Error fetching question. Let's try a new one."
				replyMarkup = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
					},
				}
			} else {
				type Option struct {
					Text   string
					Status string
				}
				
				options := []Option{
					{Text: q.CorrectOption, Status: "correct"},
					{Text: q.WrongOption1, Status: "wrong"},
					{Text: q.WrongOption2, Status: "wrong"},
				}

				randSource := rand.NewSource(time.Now().UnixNano())
				rander := rand.New(randSource)
				rander.Shuffle(len(options), func(i, j int) {
					options[i], options[j] = options[j], options[i]
				})

				responseText = fmt.Sprintf("📚 *Topic: %s*\n\n%s\n\n*A)* %s\n*B)* %s\n*C)* %s", 
					category, q.QuestionText, options[0].Text, options[1].Text, options[2].Text)

				keyboard := [][]InlineKeyboardButton{
					{
						{Text: "A", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[0].Status, q.ID, category)},
						{Text: "B", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[1].Status, q.ID, category)},
						{Text: "C", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[2].Status, q.ID, category)},
					},
				}
				replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
			}

			payload := map[string]interface{}{
				"chat_id":    userID,
				"text":       responseText,
				"parse_mode": "Markdown",
			}
			if replyMarkup != nil {
				payload["reply_markup"] = replyMarkup
			}
			
			replyBody, _ := json.Marshal(payload)
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))

		// EVENT 5: User clicked "Practice different module"
		} else if dataParts[0] == "cmd" && dataParts[1] == "categories" {
			update.Message = update.CallbackQuery.Message
			update.Message.Text = "/test"

		// EVENT 6: User clicked "Reset Module"
		} else if dataParts[0] == "reset" && len(dataParts) == 2 {
			categoryToReset := dataParts[1]

			db.Exec("UPDATE user_progress SET points = 0, attempts = 0 WHERE telegram_user_id = $1 AND category = $2", userID, categoryToReset)
			db.Exec(`
				DELETE FROM answered_questions 
				WHERE telegram_user_id = $1 
				AND question_id IN (SELECT id FROM questions WHERE category = $2)`, userID, categoryToReset)

			update.Message = update.CallbackQuery.Message
			update.Message.Text = "/progress"
		}
	} 
	
	// ==========================================
	// SCENARIO B: THE USER TYPED A MESSAGE OR SHARED CONTACT
	// ==========================================
	if update.Message != nil {
		userID := update.Message.Chat.ID
		isAuth := checkAuth(userID)

		// EVENT: User just pressed the "Share Contact" button
		if update.Message.Contact != nil {
			phone := update.Message.Contact.PhoneNumber
			
			// Format the phone number to match '9...' pattern
			phone = strings.ReplaceAll(phone, "+", "")
			phone = strings.TrimPrefix(phone, "95")
			if strings.HasPrefix(phone, "09") {
				phone = strings.TrimPrefix(phone, "0")
			}

			// Check if the formatted number exists in the database
			var dbID int
			err := db.QueryRow("SELECT id FROM authorized_users WHERE phone_number = $1", phone).Scan(&dbID)

			if err == nil {
				// SUCCESS: Update database to link this Telegram User ID
				db.Exec("UPDATE authorized_users SET telegram_user_id = $1 WHERE phone_number = $2", userID, phone)

				// 1. Send success message and remove the giant Contact button
				replyBody, _ := json.Marshal(map[string]interface{}{
					"chat_id":      userID,
					"text":         "✅ *Verification successful!*\n\nYour account has been securely linked to your phone number.",
					"parse_mode":   "Markdown",
					"reply_markup": ReplyKeyboardRemove{RemoveKeyboard: true},
				})
				http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))

				// 2. Show the Welcome screen and Start Practice button
				startBody, _ := json.Marshal(map[string]interface{}{
					"chat_id":    userID,
					"text":       "Welcome to the TKT Prep Bot! 🚀\n\nClick the button below to begin, or use /progress anytime to check your scores.",
					"reply_markup": InlineKeyboardMarkup{
						InlineKeyboard: [][]InlineKeyboardButton{
							{{Text: "Start Practicing with TKT Practice By Saya August", CallbackData: "cmd:categories"}},
						},
					},
				})
				http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(startBody))

			} else {
				// FAILED: The number is not in the database
				replyBody, _ := json.Marshal(map[string]interface{}{
					"chat_id":      userID,
					"text":         "❌ Sorry, this phone number has not been granted access yet. Please contact the administrator after purchasing.",
					"reply_markup": ReplyKeyboardRemove{RemoveKeyboard: true},
				})
				http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// EVENT: Standard text commands
		var responseText string
		var replyMarkup interface{} = nil

		if update.Message.Text == "/start" {
			if isAuth {
				responseText = "Welcome back to the TKT Prep Bot! 🚀\n\nClick the button below to begin, or use /progress anytime to check your scores."
				replyMarkup = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Start Practicing with TKT Practice By Saya August", CallbackData: "cmd:categories"}},
					},
				}
			} else {
				// User is NOT authorized. Ask for contact info.
				responseText = "🔒 *Access Restricted*\n\nTo access the TKT Practice materials, you must verify your purchase.\n\nPlease tap the button below to share your phone number securely."
				replyMarkup = ReplyKeyboardMarkup{
					Keyboard: [][]KeyboardButton{
						{{Text: "📱 Share My Phone Number", RequestContact: true}},
					},
					ResizeKeyboard:  true,
					OneTimeKeyboard: true,
				}
			}
		} else if update.Message.Text == "/test" {
			if !isAuth {
				responseText = "❌ You must be authorized to use this bot. Type /start to verify your account."
			} else {
				rows, err := db.Query("SELECT DISTINCT category FROM questions WHERE category IS NOT NULL AND category != ''")
				if err != nil {
					responseText = "Error fetching categories: " + err.Error()
				} else {
					defer rows.Close()
					responseText = "📂 *Choose a module to practice:*"
					var keyboard [][]InlineKeyboardButton
					for rows.Next() {
						var cat string
						if err := rows.Scan(&cat); err == nil {
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
		} else if update.Message.Text == "/progress" {
			if !isAuth {
				responseText = "❌ You must be authorized to use this bot. Type /start to verify your account."
			} else {
				rows, err := db.Query("SELECT category, points, attempts FROM user_progress WHERE telegram_user_id = $1", userID)
				if err != nil {
					responseText = "Error fetching your progress."
				} else {
					defer rows.Close()
					responseText = "📊 *Your Learning Progress:*\n\n"
					var keyboard [][]InlineKeyboardButton
					hasData := false
					for rows.Next() {
						var cat string
						var pts, att int
						if err := rows.Scan(&cat, &pts, &att); err == nil {
							hasData = true
							percentage := 0
							if att > 0 {
								percentage = (pts * 100) / att
							}
							responseText += fmt.Sprintf("🔹 *%s*\nScore: %d / %d (%d%%)\n\n", cat, pts, att, percentage)
							keyboard = append(keyboard, []InlineKeyboardButton{{Text: fmt.Sprintf("🔄 Reset %s", cat), CallbackData: "reset:" + cat}})
						}
					}
					if !hasData {
						responseText = "📊 *Your Learning Progress:*\n\nYou haven't completed any practice questions yet! Type /test to begin."
					} else {
						replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
					}
				}
			}
		} else {
			if !isAuth {
				responseText = "❌ You must be authorized to use this bot. Type /start to verify your account."
			} else {
				responseText = "I received your message! Type /test to practice or /progress to see your scores."
			}
		}

		payload := map[string]interface{}{
			"chat_id":    userID,
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