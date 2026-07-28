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
	MessageID int      `json:"message_id"` // Added for editing messages
	Chat      *Chat    `json:"chat"`
	Text      string   `json:"text"`
	Contact   *Contact `json:"contact"`
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
// 2. DATABASE & HELPER LOGIC
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

func checkAuth(userID int64) bool {
	var id int
	err := db.QueryRow("SELECT id FROM authorized_users WHERE telegram_user_id = $1", userID).Scan(&id)
	return err == nil
}

// DRY Helper to send requests to Telegram API
func sendTelegramRequest(apiURL, endpoint string, payload interface{}) {
	body, _ := json.Marshal(payload)
	http.Post(apiURL+endpoint, "application/json", bytes.NewBuffer(body))
}

// Persistent Main Menu Keyboard
func getMainMenu() ReplyKeyboardMarkup {
	return ReplyKeyboardMarkup{
		Keyboard: [][]KeyboardButton{
			{{Text: "📚 Practice Modules"}, {Text: "📊 My Progress"}},
		},
		ResizeKeyboard: true,
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
	// SCENARIO A: THE USER CLICKED AN INLINE BUTTON
	// ==========================================
	if update.CallbackQuery != nil {
		// Acknowledge the click immediately so the loading spinner stops
		sendTelegramRequest(apiURL, "answerCallbackQuery", map[string]string{
			"callback_query_id": update.CallbackQuery.ID,
		})

		userID := update.CallbackQuery.Message.Chat.ID
		messageID := update.CallbackQuery.Message.MessageID // Needed for editing the message
		dataParts := strings.Split(update.CallbackQuery.Data, ":")

		// SECURITY CHECK
		if !checkAuth(userID) {
			sendTelegramRequest(apiURL, "sendMessage", map[string]interface{}{
				"chat_id": userID,
				"text":    "❌ You must be authorized to use this bot. Type /start to verify your account.",
			})
			w.WriteHeader(http.StatusOK)
			return
		}

		// EVENT 1: User chose a category OR clicked "Next Question"
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
				responseText = fmt.Sprintf("🎉 *Congratulations!*\n\nYou have completed all the practice questions for *%s*.\n\nCheck your progress from the main menu.", category)
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

			// Edit the current message instead of sending a new one
			payload := map[string]interface{}{
				"chat_id":    userID,
				"message_id": messageID,
				"text":       responseText,
				"parse_mode": "Markdown",
			}
			if replyMarkup != nil {
				payload["reply_markup"] = replyMarkup
			}
			sendTelegramRequest(apiURL, "editMessageText", payload)

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

			var explanation, qText string
			db.QueryRow("SELECT question_text, explanation FROM questions WHERE id = $1", questionID).Scan(&qText, &explanation)

			var responseText string
			var nextKeyboard InlineKeyboardMarkup

			if status == "correct" {
				responseText = fmt.Sprintf("✅ *Correct!*\n\n_%s_\n\n*💡 Explanation:*\n%s", qText, explanation)
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
					},
				}
			} else {
				responseText = fmt.Sprintf("❌ *Incorrect!*\n\n_%s_\n\n*💡 Hint:*\n%s", qText, explanation)
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Try Again 🔄", CallbackData: fmt.Sprintf("retry:%s:%s", questionID, category)}},
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
					},
				}
			}

			// Edit message to show feedback
			sendTelegramRequest(apiURL, "editMessageText", map[string]interface{}{
				"chat_id":      userID,
				"message_id":   messageID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})

		// EVENT 3: User clicked an answer AFTER hitting Try Again
		} else if dataParts[0] == "retryans" && len(dataParts) == 4 {
			status := dataParts[1]
			questionID := dataParts[2]
			category := dataParts[3]

			var explanation, qText string
			db.QueryRow("SELECT question_text, explanation FROM questions WHERE id = $1", questionID).Scan(&qText, &explanation)

			var responseText string
			var nextKeyboard InlineKeyboardMarkup

			if status == "correct" {
				responseText = fmt.Sprintf("✅ *Correct!*\n\n_%s_\n\n*💡 Explanation:*\n%s", qText, explanation)
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
					},
				}
			} else {
				responseText = fmt.Sprintf("❌ *Incorrect!*\n\n_%s_\n\n*💡 Hint:*\n%s", qText, explanation)
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
					},
				}
			}

			sendTelegramRequest(apiURL, "editMessageText", map[string]interface{}{
				"chat_id":      userID,
				"message_id":   messageID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})

		// EVENT 4: User clicked "Try Again"
		} else if dataParts[0] == "retry" && len(dataParts) == 3 {
			questionID := dataParts[1]
			category := dataParts[2]

			var q TKTQuestion
			err := db.QueryRow(`
                SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2 
                FROM questions 
                WHERE id = $1`, questionID).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2)

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

				responseText = fmt.Sprintf("🔄 *Try Again!*\n\n📚 *Topic: %s*\n\n%s\n\n*A)* %s\n*B)* %s\n*C)* %s",
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

			sendTelegramRequest(apiURL, "editMessageText", map[string]interface{}{
				"chat_id":      userID,
				"message_id":   messageID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": replyMarkup,
			})

		// EVENT 5: User clicked "Reset Module"
		} else if dataParts[0] == "reset" && len(dataParts) == 2 {
			categoryToReset := dataParts[1]

			db.Exec("UPDATE user_progress SET points = 0, attempts = 0 WHERE telegram_user_id = $1 AND category = $2", userID, categoryToReset)
			db.Exec(`
                DELETE FROM answered_questions 
                WHERE telegram_user_id = $1 
                AND question_id IN (SELECT id FROM questions WHERE category = $2)`, userID, categoryToReset)

			sendTelegramRequest(apiURL, "editMessageText", map[string]interface{}{
				"chat_id":    userID,
				"message_id": messageID,
				"text":       fmt.Sprintf("🔄 *%s* has been reset. You can now practice it from the beginning.", categoryToReset),
				"parse_mode": "Markdown",
			})
		}
	}

	// ==========================================
	// SCENARIO B: THE USER TYPED A MESSAGE OR SHARED CONTACT
	// ==========================================
	if update.Message != nil {
		userID := update.Message.Chat.ID
		isAuth := checkAuth(userID)
		text := update.Message.Text

		// EVENT: User shared their contact info
		if update.Message.Contact != nil {
			phone := update.Message.Contact.PhoneNumber

			phone = strings.ReplaceAll(phone, "+", "")
			phone = strings.TrimPrefix(phone, "95")
			if strings.HasPrefix(phone, "09") {
				phone = strings.TrimPrefix(phone, "0")
			}

			var dbID int
			err := db.QueryRow("SELECT id FROM authorized_users WHERE phone_number = $1", phone).Scan(&dbID)

			if err == nil {
				db.Exec("UPDATE authorized_users SET telegram_user_id = $1 WHERE phone_number = $2", userID, phone)

				// Clear the contact button and show the new Persistent Menu
				sendTelegramRequest(apiURL, "sendMessage", map[string]interface{}{
					"chat_id":      userID,
					"text":         "✅ *Verification successful!*\n\nWelcome to the TKT Prep Bot! Use the menu below to navigate.",
					"parse_mode":   "Markdown",
					"reply_markup": getMainMenu(),
				})
			} else {
				sendTelegramRequest(apiURL, "sendMessage", map[string]interface{}{
					"chat_id":      userID,
					"text":         "❌ Sorry, this phone number has not been granted access yet. Please contact the administrator after purchasing.",
					"reply_markup": ReplyKeyboardRemove{RemoveKeyboard: true},
				})
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// EVENT: Standard text commands or Menu Button clicks
		var responseText string
		var replyMarkup interface{} = nil

		if text == "/start" {
			if isAuth {
				responseText = "Welcome back! 🚀\n\nUse the menu below to start practicing or check your scores."
				replyMarkup = getMainMenu()
			} else {
				responseText = "🔒 *Access Restricted*\n\nTo access the TKT Practice materials, you must verify your purchase.\n\nPlease tap the button below to share your phone number securely."
				replyMarkup = ReplyKeyboardMarkup{
					Keyboard: [][]KeyboardButton{
						{{Text: "📱 Share My Phone Number", RequestContact: true}},
					},
					ResizeKeyboard:  true,
					OneTimeKeyboard: true,
				}
			}
		} else if text == "📚 Practice Modules" || text == "/test" {
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
							keyboard = append(keyboard, []InlineKeyboardButton{{Text: "📘 " + cat, CallbackData: "cat:" + cat}})
						}
					}
					if len(keyboard) > 0 {
						replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
					} else {
						responseText = "No modules found. Please add data to your Supabase table!"
					}
				}
			}
		} else if text == "📊 My Progress" || text == "/progress" {
			if !isAuth {
				responseText = "❌ You must be authorized to use this bot. Type /start to verify your account."
			} else {
				rows, err := db.Query("SELECT category, points, attempts FROM user_progress WHERE telegram_user_id = $1 ORDER BY category", userID)
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
						responseText = "📊 *Your Learning Progress:*\n\nYou haven't completed any practice questions yet! Tap 'Practice Modules' to begin."
					} else {
						responseText += "\n_Need a fresh start? Use the buttons below to reset a module's progress._"
						replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
					}
				}
			}
		} else {
			if !isAuth {
				responseText = "❌ You must be authorized to use this bot. Type /start to verify your account."
			} else {
				responseText = "I didn't understand that command. Please use the menu below."
				replyMarkup = getMainMenu()
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

		sendTelegramRequest(apiURL, "sendMessage", payload)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}