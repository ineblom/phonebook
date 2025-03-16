package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/arangodb/go-driver/v2/arangodb"
	"github.com/arangodb/go-driver/v2/arangodb/shared"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/nyaruka/phonenumbers"
)

type VerificationAttempt struct {
	Key       string    `json:"-"`
	Number    string    `json:"number"`
	Code      string    `json:"code"`
	CreatedAt time.Time `json:"created_at"`
}

type RequestVerificationRequest struct {
	Number string `json:"number"`
}

type CancelVerificationRequest struct {
	AttemptKey string `json:"attempt_key"`
}

type VerifyRequest struct {
	AttemptKey string `json:"attempt_key"`
	Code       string `json:"code"`
}

const VerificationExpiryTime = time.Minute * 5
const JWTSecret = "MyAwesomeSecretForJWT"

func CreateVerificationAttempt(ctx context.Context, db *Database, number string) (string, error) {
	// TODO: Ensure only one verification attempt per user.
	// Delete old ones on create or use number for key?

	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n)

	attempt := VerificationAttempt{
		Number:    number,
		Code:      code,
		CreatedAt: time.Now(),
	}

	doc, err := db.verification_attempts.CreateDocument(ctx, attempt)
	if err != nil {
		return "", err
	}

	log.Printf("%v code is %v\n", doc.ID, code)

	return doc.Key, nil
}

func VerifyCode(ctx context.Context, db *Database, attempt *VerificationAttempt, code string) (bool, error) {
	if time.Now().After(attempt.CreatedAt.Add(VerificationExpiryTime)) {
		db.verification_attempts.DeleteDocument(ctx, attempt.Key)
		return false, nil
	}

	if attempt.Code == code {
		db.verification_attempts.DeleteDocument(ctx, attempt.Key)
		return true, nil
	}

	return false, nil
}

func CreateUser(ctx context.Context, db *Database, number string) (string, error) {
	user := User{Number: number}

	meta, err := db.users.CreateDocument(ctx, user)
	if err != nil {
		return "", err
	}

	return meta.Key, nil
}

func GetOrCreateUser(ctx context.Context, db *Database, number string) (string, error) {
	query := "FOR u IN users FILTER u.number == @number LIMIT 1 RETURN u"
	opts := arangodb.QueryOptions{
		BindVars: map[string]interface{}{
			"number": number,
		},
	}
	cursor, err := db.phonebook.Query(ctx, query, &opts)
	if err != nil {
		return "", err
	}
	defer cursor.Close()

	var result string

	if cursor.HasMore() {
		var usr User
		meta, err := cursor.ReadDocument(ctx, &usr)
		if err != nil {
			return "", err
		}
		result = meta.Key
	} else {
		newContactUserKey, err := CreateUser(ctx, db, number)
		if err != nil {
			return "", err
		}
		result = newContactUserKey
	}

	return result, nil
}

func RequestVerificationHandler(db *Database) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancelCtx := context.WithTimeout(c.UserContext(), time.Second*30)
		defer cancelCtx()

		var req RequestVerificationRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid request",
			})
		}

		number, err := phonenumbers.Parse(req.Number, "SE")
		if err != nil {
			log.Printf("Failed to parse phone number: %v", err)
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid phone number",
			})
		}

		if !phonenumbers.IsValidNumberForRegion(number, "SE") {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid phone number for region.",
			})
		}

		formattedNumber := phonenumbers.Format(number, phonenumbers.E164)

		attemptKey, err := CreateVerificationAttempt(ctx, db, formattedNumber)
		if err != nil {
			log.Printf("Failed to create verification attempt: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to create verification attempt",
			})
		}

		return c.JSON(fiber.Map{
			"message": "Verifcation code sent",
			"id":      attemptKey,
		})
	}
}

func CancelVerificationHandler(db *Database) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancelCtx := context.WithTimeout(c.UserContext(), time.Second*30)
		defer cancelCtx()

		var req CancelVerificationRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid request",
			})
		}

		_, err := db.verification_attempts.DeleteDocument(ctx, req.AttemptKey)
		if shared.IsNotFound(err) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "Attempt not found",
			})
		} else if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to cancel verification attempt",
			})
		}

		return c.JSON(fiber.Map{
			"message": "Verification canceled",
		})
	}
}

func GenerateJWT(userKey string) (string, error) {
	expirationTime := time.Now().Add(time.Hour * 24 * 30).Unix()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"exp": expirationTime,
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),

		"user_key": userKey,
	})
	jwt, err := token.SignedString([]byte(JWTSecret))
	if err != nil {
		return "", err
	}

	return jwt, nil
}

func VerifyRequestHandler(db *Database) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancelCtx := context.WithTimeout(c.UserContext(), time.Second*30)
		defer cancelCtx()

		var req VerifyRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid request",
			})
		}

		var attempt VerificationAttempt
		_, err := db.verification_attempts.ReadDocument(ctx, req.AttemptKey, &attempt)
		if err != nil {
			log.Printf("Failed to get verification attempt: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to verify code",
			})
		}

		attempt.Key = req.AttemptKey

		valid, err := VerifyCode(ctx, db, &attempt, req.Code)
		if err != nil {
			log.Printf("Failed to verify code: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to verify code",
			})
		}

		if !valid {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Invalid or expired verification code",
			})
		}

		userKey, err := GetOrCreateUser(ctx, db, attempt.Number)
		if err != nil {
			log.Printf("Failed to get or create user: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to log in to user account",
			})
		}

		jwt, err := GenerateJWT(userKey)
		if err != nil {
			log.Printf("Failed to sign JWT: %v", err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to log in to user account",
			})
		}

		return c.JSON(fiber.Map{
			"message": "User verified and created successfully",
			"token":   jwt,
		})
	}
}

func AuthMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Missing authorization header",
			})
		}

		tokenString := authHeader[7:]

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return "", fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(JWTSecret), nil
		})

		if err != nil || !token.Valid {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Invalid or expired token",
			})
		}

		claims := token.Claims.(jwt.MapClaims)
		c.Locals("userKey", claims["user_key"])

		return c.Next()
	}
}
