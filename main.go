package main

import (
	"context"
	"log"
	"strings"

	"github.com/arangodb/go-driver/v2/arangodb"
	"github.com/arangodb/go-driver/v2/arangodb/shared"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/nyaruka/phonenumbers"
)

type User struct {
	Number string `json:"number"`
}

type Contact struct {
	CountryCode string `json:"country_code"`
	Number      string `json:"number"`
	Name        string `json:"name"`
}

type AddContactsRequest struct {
	Contacts []Contact `json:"contacts"`
}

type ContactEdge struct {
	From string `json:"_from"`
	To   string `json:"_to"`
	Name string `json:"name"`
}

func AddContactsHandler(db *Database) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var request AddContactsRequest
		if err := c.BodyParser(&request); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
		}

		userKey := c.Locals("userKey").(string)

		ctx := c.UserContext()

		for _, contact := range request.Contacts {
			countryCode := strings.ToUpper(contact.CountryCode)

			number, err := phonenumbers.Parse(contact.Number, countryCode)
			if err != nil {
				log.Printf("Failed to parse phone number: %v %v - %v", countryCode, contact.Number, err)
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid phone number"})
			}

			if !phonenumbers.IsValidNumberForRegion(number, countryCode) {
				log.Printf("Provided invalid phone number, skipping...")
				continue
			}

			formattedNumber := phonenumbers.Format(number, phonenumbers.E164)

			contactUserKey, err := GetOrCreateUser(ctx, db, formattedNumber)
			if err != nil {
				log.Printf("Failed to GetOrCreateUser in add contacts handler: %v", err)
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create contact relationship"})
			}

			query := "FOR c IN contacts FILTER c._from == @from AND c._to == @to LIMIT 1 RETURN c"
			opts := arangodb.QueryOptions{
				BindVars: map[string]interface{}{
					"from": "users/" + userKey,
					"to":   "users/" + contactUserKey,
				},
			}
			cursor, err := db.phonebook.Query(ctx, query, &opts)
			if err != nil {
				log.Printf("Failed to query existing contacts: %v", err)
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to check existing contacts"})
			}
			defer cursor.Close()

			if cursor.HasMore() {
				var existingEdge ContactEdge
				meta, err := cursor.ReadDocument(ctx, &existingEdge)
				if err != nil {
					log.Printf("Failed to read existing contact: %v", err)
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to read existing contact"})
				}

				if existingEdge.Name != contact.Name {
					existingEdge.Name = contact.Name
					_, err = db.contacts.UpdateDocument(ctx, meta.Key, existingEdge)
					if err != nil {
						log.Printf("Failed to update contact name: %v", err)
						return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update contact name"})
					}
				}

				continue
			}

			edge := ContactEdge{
				From: "users/" + userKey,
				To:   "users/" + contactUserKey,
				Name: contact.Name,
			}

			_, err = db.contacts.CreateDocument(ctx, edge)
			if err != nil {
				log.Printf("Failed to create contact edge: %v", err)
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create contact relationship"})
			}
		}

		return c.JSON(fiber.Map{"message": "Contacts added"})
	}
}

type GetContactsRequest struct {
	UserKey string `json:"user_key"`
}

type GetContactsResult struct {
	Name    string `json:"name"`
	UserKey string `json:"user_key"`
}

func GetContactsHandler(db *Database) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := c.UserContext()
		var request GetContactsRequest
		if err := c.BodyParser(&request); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
		}

		query := "FOR v, e in 1..1 OUTBOUND @user contacts RETURN { name: e.name, user_key: v._key }"
		opts := arangodb.QueryOptions{
			BindVars: map[string]interface{}{
				"user": "users/" + request.UserKey,
			},
		}
		cursor, err := db.phonebook.Query(ctx, query, &opts)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to get contacts"})
		}
		defer cursor.Close()

		var result []GetContactsResult

		for {
			var contact GetContactsResult
			_, err := cursor.ReadDocument(ctx, &contact)

			if shared.IsNoMoreDocuments(err) {
				break
			} else if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to read contact"})
			}

			result = append(result, contact)
		}

		return c.JSON(result)
	}
}

func RunWebServer(db *Database) {
	app := fiber.New()

	// Add CORS middleware
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS",
	}))

	app.Get("/ping", func(c *fiber.Ctx) error {
		return c.SendString("pong")
	})

	app.Post("/request-verification", RequestVerificationHandler(db))
	app.Post("/cancel-verification", CancelVerificationHandler(db))
	app.Post("/verify", VerifyRequestHandler(db))

	api := app.Group("/api")
	api.Use(AuthMiddleware())

	api.Get("/me", func(c *fiber.Ctx) error {
		userKey := c.Locals("userKey").(string)

		var user User
		_, err := db.users.ReadDocument(c.UserContext(), userKey, &user)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
		}

		return c.JSON(fiber.Map{
			"user_key": userKey,
			"number":   user.Number,
		})
	})

	api.Post("/add-contacts", AddContactsHandler(db))
	api.Get("/contacts", GetContactsHandler(db))

	app.Listen(":3000")
}

/*
Outline:
  Users sign up using their phone number. Only one user per phone number.
  They provide their contacts and this is used to build a graph.

UX:
  1. Enter phone number / app grabs number
  2. Verify number with SMS code
  3. Ask for contacts and upload to server
  4. Show graph of contacts etc.

TODO:
	-	Make sure to format submitted numbers, match users correctly.
		Add country to request verification.

	- Protect accounts with email/password, 2FA, etc. Phone number recycling
		could give users access to other people's accounts. Look into further.
	- Latest user that signed up using number gets the number.

*/

func main() {
	ctx := context.Background()

	db := SetupDBClient(ctx)

	RunWebServer(&db)
}
