package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/dgrijalva/jwt-go"
	"github.com/go-redis/redis"
	"github.com/gofrs/uuid"
	"github.com/julienschmidt/httprouter"
	"github.com/rso-bicycle/users/models"
	"github.com/volatiletech/sqlboiler/boil"
	"github.com/volatiletech/sqlboiler/queries/qm"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"io/ioutil"
	"net/http"
	"time"
)

var (
	internalError = &errorResponse{
		Status:  http.StatusInternalServerError,
		Code:    "internal_error",
		Message: "There was an internal error while processing the request",
	}
	unprocessableError = &errorResponse{
		Status:  http.StatusUnprocessableEntity,
		Code:    "unprocessable",
		Message: "Request could not be processed",
	}
)

type errorResponse struct {
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func writeErrorResponse(w http.ResponseWriter, req *http.Request, res *errorResponse) {
	res.RequestID = req.Header.Get("x-request-id")
	b, err := json.Marshal(res)
	if err != nil {
		panic(err.Error())
	}
	w.WriteHeader(res.Status)
	w.Write(b)
}

func createPublicRouter(log *zap.Logger, db *sql.DB, client *redis.Client) *httprouter.Router {
	type registerRequest struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	type loginRequest struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	type activateRequest struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}

	router := httprouter.New()

	// Create a register route
	router.POST("/register", func(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
		b, err := ioutil.ReadAll(req.Body)
		if err != nil {
			log.Error("reading register body", zap.Error(err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		rq := new(registerRequest)
		if err := json.Unmarshal(b, rq); err != nil {
			log.Error("unmarshaling register body", zap.Error(err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if val, err := bcrypt.GenerateFromPassword([]byte(rq.Password), bcrypt.DefaultCost); err != nil {
			log.Error("bcrypting password", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		} else {
			rq.Password = string(val)
		}

		// Generate an activation code
		var bt [8]byte
		rand.Read(bt[:])
		activationCode := hex.EncodeToString(bt[:])
		uid, _ := uuid.NewV4()

		// TODO: add validation or w/e
		m := models.User{
			UID:                    uid.String(),
			Username:               rq.Email,
			Password:               rq.Password,
			ActivationCode:         activationCode,
			ActivationCodeValidity: time.Now().Add(time.Hour * 24),
		}

		if err := m.Insert(db, boil.Infer()); err != nil {
			log.Error("storing user", zap.Error(err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// TODO(uh): Send the event to Kafka

		w.WriteHeader(http.StatusNoContent)
	})
	// Create a login route
	router.POST("/login", func(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
		b, err := ioutil.ReadAll(req.Body)
		if err != nil {
			log.Error("reading login body", zap.Error(err))
			writeErrorResponse(w, req, unprocessableError)
			return
		}

		rq := new(loginRequest)
		if err := json.Unmarshal(b, rq); err != nil {
			log.Error("unmarshaling login body", zap.Error(err))
			writeErrorResponse(w, req, unprocessableError)
			return
		}

		user, err := models.Users(qm.Where("email = ?", rq.Email)).One(db)
		if err != nil {
			log.Error("loading login user", zap.Error(err))
			writeErrorResponse(w, req, internalError)
			return
		}

		switch err {
		case sql.ErrNoRows:
			log.Error("user does not exist")
			writeErrorResponse(w, req, &errorResponse{
				Status:  http.StatusBadRequest,
				Code:    "invalid_user_or_password",
				Message: "Invalid user or password",
			})
			return
		default:
			log.Error("checking activation code", zap.Error(err))
			writeErrorResponse(w, req, internalError)
			return
		}

		// Validate the password
		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(rq.Password)); err != nil {
			writeErrorResponse(w, req, &errorResponse{
				Status:  http.StatusBadRequest,
				Code:    "invalid_user_or_password",
				Message: "Invalid user or password",
			})
			return
		}

		// Issue a JWT token and store it.
		// Create the Claims
		claims := &jwt.StandardClaims{
			ExpiresAt: 15000,
			Issuer:    "rso-bicycle:users",
		}
		jwtToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("signingkey"))
		if err != nil {
			log.Error("creating JWT token", zap.Error(err))
			writeErrorResponse(w, req, internalError)
			return
		}

		var bt [24]byte
		rand.Read(bt[:])
		token := hex.EncodeToString(bt[:])

		if err := client.Set("token:"+token, jwtToken, time.Hour*24*7).Err(); err != nil {
			log.Error("storing access token", zap.Error(err))
			writeErrorResponse(w, req, internalError)
		} else {
			w.Header().Set("Authorization", "Bearer "+token)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	// Activate the account
	router.POST("/activate", func(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
		b, err := ioutil.ReadAll(req.Body)
		if err != nil {
			log.Error("reading activate body", zap.Error(err))
			writeErrorResponse(w, req, unprocessableError)
			return
		}

		rq := new(activateRequest)
		if err := json.Unmarshal(b, rq); err != nil {
			log.Error("unmarshaling activate body", zap.Error(err))
			writeErrorResponse(w, req, unprocessableError)
			return
		}

		user, err := models.Users(qm.Where("email = ? AND activation_code = ? AND activated = false AND activation_code_validity > NOW()", rq.Email, rq.Code)).One(db)

		switch err {
		case sql.ErrNoRows:
			log.Error("activation code does not exist")
			writeErrorResponse(w, req, &errorResponse{
				Status:  http.StatusBadRequest,
				Code:    "invalid_activation_code",
				Message: "The activation code is invalid",
			})
			return
		default:
			log.Error("checking activation code", zap.Error(err))
			writeErrorResponse(w, req, internalError)
			return
		}

		// Exists. Activate the account.
		user.Activated = true
		if _, err := user.Update(db, boil.Infer()); err != nil {
			log.Error("activating user", zap.Error(err))
			writeErrorResponse(w, req, internalError)
			return
		}

		// TODO(uh): Send an event that the account was activated

		w.WriteHeader(http.StatusNoContent)
	})

	// List all the users
	router.GET("/", func(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
		if us, err := models.Users().All(db); err != nil {
			log.Error("loading users", zap.Error(err))
			writeErrorResponse(w, req, internalError)
		} else {
			if us == nil {
				us = models.UserSlice{}
			}
			b, _ := json.MarshalIndent(us, "", "  ")
			w.Header().Set("content-type", "application/json")
			w.Write(b)
		}
	})

	return router
}
