package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	ctrl "minitwit/controllers"
	mntr "minitwit/monitoring"
)

type Response struct {
	Status int
}

var (
	db     *sql.DB
	latest = 0
)

const (
	port = 8000
)

func main() {
	ctrl.InitDB(ctrl.InitDBSchema, ctrl.DBPath)
	r := mux.NewRouter()

	// Endpoints
	r.HandleFunc("/api/latest", getLatest)
	r.HandleFunc("/api/register", register)
	r.HandleFunc("/api/fllws/{username}", follow)
	r.HandleFunc("/api/msgs/{username}", messagesPerUser)
	r.HandleFunc("/api/msgs", messages)
	r.HandleFunc("/api/msgs/{username}", messagesPerUser)

	// Register r as HTTP handler
	http.Handle("/", mntr.MiddlewareMetrics(r, true))

	/*
		Prometheus metrics setup
	*/

	http.Handle("/metrics", promhttp.Handler())

	// Use goroutine because http.ListenAndServe() is a blocking method
	go func() {
		if err := http.ListenAndServe(":2112", nil); err != nil {
			log.Fatal("Error: ", err)
		}
	}()

	/*
		Start API server
	*/

	srv := &http.Server{
		Addr:         "0.0.0.0:" + strconv.Itoa(port),
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
	}

	db = ctrl.ConnectDB(ctrl.DBPath)
	log.Printf("Starting API on port %d\n", port)

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal("Error: ", err)
	}
}

func logQueryInfo(res sql.Result, query string, queryData string) {
	log.Printf(query, queryData)
	affected, _ := res.RowsAffected()
	lastInsert, _ := res.LastInsertId()
	log.Printf("	affected rows: %d, LastInsertId: %d", affected, lastInsert)
}

func notReqFromSimulator(w http.ResponseWriter, r *http.Request) []byte {
	if r.Header.Get("Authorization") != os.Getenv("SIM_AUTH") {
		w.Header().Set("Content-Type", "application/json")

		response, _ := json.Marshal(map[string]interface{}{
			"status": http.StatusForbidden,
			"error":  "You are not authorized to use this resource!",
		})

		w.Write(response)
		return response
	}

	return nil
}

func updateLatest(r *http.Request) {
	params := r.URL.Query()
	def := -1
	val := def
	if params.Get("latest") != "" {
		val, _ = strconv.Atoi(params.Get("latest"))
	}

	if val != -1 {
		latest = val
	}
}
func getLatest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	latest_struct := struct {
		Latest int `json:"latest"`
	}{
		latest,
	}
	resp, _ := json.Marshal(latest_struct)
	w.Write(resp)
}

func register(w http.ResponseWriter, r *http.Request) {
	log.Println("REGISTER:")
	updateLatest(r)

	request_data := json.NewDecoder(r.Body)

	r_data := struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Pwd      string `json:"pwd"`
	}{}

	request_data.Decode(&r_data)
	var status int
	var errorMsg string
	if r.Method == "POST" {
		if r_data.Username == "" {
			errorMsg = "You have to enter a username"
			status = 400
		} else if r_data.Email == "" || !strings.Contains(r_data.Email, "@") {
			errorMsg = "You have to enter a valid email address"
			status = 400
		} else if r_data.Pwd == "" {
			errorMsg = "You have to enter a password"
			status = 400
		} else if ctrl.GetUserID(r_data.Username, db) != -1 {
			errorMsg = "The username is already taken"
			status = 400
		} else {
			status = 204
			db := ctrl.ConnectDB(ctrl.DBPath)
			hashed_pw, err := ctrl.HashPw(r_data.Pwd)
			ctrl.CheckError(err)

			query := "INSERT INTO user (username, email, pw_hash) VALUES (?, ?, ?)"
			res, err := db.Exec(query, r_data.Username, r_data.Email, hashed_pw)
			ctrl.CheckError(err)
			logQueryInfo(res, "	Inserting user \"%s\" into database\n", r_data.Username)
		}
	}
	log.Println(errorMsg)
	resp, _ := json.Marshal(204)
	w.WriteHeader(status)
	w.Write(resp)
}

func messages(w http.ResponseWriter, r *http.Request) {
	updateLatest(r)

	not_from_sim_response := notReqFromSimulator(w, r)

	if not_from_sim_response != nil {
		w.WriteHeader(403)
		w.Write(not_from_sim_response)
		return
	}

	def := 100
	vars := mux.Vars(r)
	val := def

	if len(vars) != 0 {
		val, _ = strconv.Atoi(vars["no"])
	}

	no_msgs := val

	if r.Method == "GET" {
		query := "SELECT message.*, user.* FROM message, user WHERE message.flagged = 0 AND message.author_id = user.user_id ORDER BY message.pub_date DESC LIMIT ?"
		rows, err := db.Query(query, no_msgs)
		messages := ctrl.HandleQuery(rows, err)

		var filtered_msgs []ctrl.Message

		for _, m := range messages {
			filtered_msg := ctrl.Message{
				ID:       m["message_id"].(uint),
				AuthorID: m["author_id"].(int),
				Text:     m["text"].(string),
				Date:     m["pub_date"].(int64),
				Flagged:  m["flagged"].(uint8),
			}

			filtered_msgs = append(filtered_msgs, filtered_msg)
		}

		log.Println(len(filtered_msgs))
		resp, _ := json.Marshal(filtered_msgs)
		w.WriteHeader(200)
		w.Write(resp)
	} else {
		w.WriteHeader(405) // Method Not Allowed
	}
}

func messagesPerUser(w http.ResponseWriter, r *http.Request) {
	log.Println("TWEET:")
	updateLatest(r)

	not_from_sim_response := notReqFromSimulator(w, r)

	if not_from_sim_response != nil {
		w.WriteHeader(403)
		w.Write(not_from_sim_response)
	}

	def := 100
	vars := mux.Vars(r)
	val := def
	if len(vars) != 0 {
		val, _ = strconv.Atoi(vars["no"])
	}

	no_msgs := val

	if r.Method == "GET" {
		query := "SELECT message.*, user.* FROM message, user  WHERE message.flagged = 0 AND user.user_id = message.author_id AND user.user_id = ? ORDER BY message.pub_date DESC LIMIT ?"
		rows, err := db.Query(query, no_msgs)
		messages := ctrl.HandleQuery(rows, err)

		var filtered_msgs []ctrl.Message

		for _, m := range messages {
			filtered_msg := ctrl.Message{
				ID:       m["message_id"].(uint),
				AuthorID: m["author_id"].(int),
				Text:     m["text"].(string),
				Date:     m["pub_date"].(int64),
				Flagged:  m["flagged"].(uint8),
			}

			filtered_msgs = append(filtered_msgs, filtered_msg)
		}

		resp, _ := json.Marshal(filtered_msgs)
		w.WriteHeader(204)
		w.Write(resp)
		return

	} else if r.Method == "POST" {
		r_data := struct {
			Content string `json:"content"`
		}{}

		username := mux.Vars(r)["username"]
		json.NewDecoder(r.Body).Decode(&r_data)

		rData := ctrl.Message{
			AuthorID: ctrl.GetUserID(username, db),
			Text:     r_data.Content,
			Date:     time.Now().Unix(),
		}

		query := "INSERT INTO message (author_id, text, pub_date, flagged) VALUES (?, ?, ?, 0)"
		if res, err := db.Exec(query, rData.AuthorID, rData.Text, rData.Date); err != nil {
			resp, _ := json.Marshal(Response{Status: 403})
			w.WriteHeader(403)
			w.Write(resp)
			return
		} else {
			logQueryInfo(res, "	Inserting message \"%s\" into database\n", rData.Text)
			resp, _ := json.Marshal(Response{Status: 204})
			w.WriteHeader(204)
			w.Write(resp)
		}
	}
}

func follow(w http.ResponseWriter, r *http.Request) {
	log.Println("FOLLOW/UNFOLLOW:")
	username := mux.Vars(r)["username"]
	updateLatest(r)
	decoder := json.NewDecoder(r.Body)

	not_from_sim_response := notReqFromSimulator(w, r)
	if not_from_sim_response != nil {
		w.WriteHeader(403)
		w.Write(not_from_sim_response)
		return
	}

	user_id := ctrl.GetUserID(username, db)
	if user_id == -1 {
		status := 404
		resp, _ := json.Marshal(Response{Status: status})
		w.WriteHeader(status)
		w.Write(resp)
		return
	}

	type fReq struct {
		Follow   string `json:"follow"`
		Unfollow string `json:"unfollow"`
	}
	req := fReq{}
	decoder.Decode(&req)

	if req.Follow != "" && r.Method == "POST" {
		follows_user_id := ctrl.GetUserID(req.Follow, db)
		if follows_user_id == -1 {
			status := 404
			resp, _ := json.Marshal(Response{Status: status})
			w.WriteHeader(status)
			w.Write(resp)
			return
		}

		query := "INSERT INTO follower (who_id, whom_id) VALUES (?, ?)"
		if res, err := db.Exec(query, user_id, follows_user_id); err != nil {
			resp, _ := json.Marshal(Response{Status: 403})
			w.WriteHeader(403)
			w.Write(resp)
			return
		} else {
			logQueryInfo(res, "	Inserting follower \"%s\" into database\n", req.Follow)
			resp, _ := json.Marshal(Response{Status: 204})
			w.WriteHeader(204)
			w.Write(resp)
		}

		return
	} else if req.Unfollow != "" && r.Method == "POST" {
		unfollows_username := req.Unfollow
		unfollows_user_id := ctrl.GetUserID(unfollows_username, db)

		if unfollows_user_id == -1 {
			resp, _ := json.Marshal(Response{Status: 404})
			w.WriteHeader(404)
			w.Write(resp)
			return
		}

		query := "DELETE FROM follower WHERE who_id=? and WHOM_ID=?"
		if res, err := db.Exec(query, user_id, unfollows_user_id); err != nil {
			resp, _ := json.Marshal(Response{Status: 403})
			w.WriteHeader(403)
			w.Write(resp)
			return
		} else {
			logQueryInfo(res, "	Deleting follower \"%s\" from database\n", unfollows_username)
			resp, _ := json.Marshal(Response{Status: 204})
			w.WriteHeader(204)
			w.Write(resp)
		}

		return
	} else if r.Method == "GET" {
		def := 100
		vars := mux.Vars(r)
		val := def
		if len(vars) != 0 {
			val, _ = strconv.Atoi(vars["no"])
		}

		query := "SELECT user.username FROM user INNER JOIN follower ON follower.whom_id=user.user_id WHERE follower.who_id=? LIMIT ?"
		var followers []map[string]interface{}
		if rows, err := db.Query(query, user_id, val); err != nil {
			resp, _ := json.Marshal(Response{Status: 403})
			w.WriteHeader(403)
			w.Write(resp)
			return
		} else {
			followers = ctrl.HandleQuery(rows, err)
		}

		var follower_names []interface{}
		for f := range followers {
			follower_names = append(follower_names, f)
		}

		followers_response := struct {
			Follows []interface{} `json:"follows"`
		}{
			Follows: follower_names,
		}

		resp, _ := json.Marshal(followers_response)
		w.WriteHeader(204)
		w.Write(resp)
	}
}
