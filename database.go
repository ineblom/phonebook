package main

import (
	"context"
	"log"

	"github.com/arangodb/go-driver/v2/arangodb"
	"github.com/arangodb/go-driver/v2/arangodb/shared"
	"github.com/arangodb/go-driver/v2/connection"
)

const DatabaseName = "phonebook"

type Database struct {
	phonebook             arangodb.Database
	users                 arangodb.Collection
	contacts              arangodb.Collection
	verification_attempts arangodb.Collection
}

func getCollection(ctx context.Context, db arangodb.Database, name string, colType arangodb.CollectionType) (arangodb.Collection, error) {
	exists, err := db.CollectionExists(ctx, name)
	if err != nil {
		return nil, err
	}

	var col arangodb.Collection

	if exists {
		col, err = db.GetCollection(ctx, name, nil)
		if err != nil {
			return nil, err
		}
	} else {
		props := arangodb.CreateCollectionProperties{
			Type: colType,
		}
		col, err = db.CreateCollection(ctx, name, &props)
		if err != nil {
			return nil, err
		}
	}

	return col, nil
}

func SetupDBClient(ctx context.Context) Database {
	endpoint := connection.NewRoundRobinEndpoints([]string{"http://localhost:8529"})
	conn := connection.NewHttp2Connection(connection.DefaultHTTP2ConfigurationWrapper(endpoint, true))

	auth := connection.NewBasicAuth("root", "openSesame")
	err := conn.SetAuthentication(auth)
	if err != nil {
		log.Fatalf("Failed to set authentication %v", err)
	}

	client := arangodb.NewClient(conn)

	var db Database

	// Ensure database exists
	dbExists, err := client.DatabaseExists(ctx, DatabaseName)
	if err != nil {
		log.Fatalf("Failed to check if database exists: %v", err)
	}

	if dbExists {
		db.phonebook, err = client.GetDatabase(ctx, DatabaseName, nil)
		if err != nil {
			log.Fatalf("Failed to get database: %v", err)
		}
	} else {
		db.phonebook, err = client.CreateDatabase(ctx, DatabaseName, nil)
		if err != nil && !shared.IsConflict(err) {
			log.Fatalf("Failed to create database: %v", err)
		}
	}

	// Ensure user collection exists
	db.users, err = getCollection(ctx, db.phonebook, "users", arangodb.CollectionTypeDocument)
	if err != nil {
		log.Fatalf("Failed to create users collection: %v", err)
	}

	// Ensure contacts collection exists
	db.contacts, err = getCollection(ctx, db.phonebook, "contacts", arangodb.CollectionTypeEdge)
	if err != nil && !shared.IsConflict(err) {
		log.Fatalf("Failed to create contacts collection: %v", err)
	}

	// Ensure verification collection exists
	db.verification_attempts, err = getCollection(ctx, db.phonebook, "verification_attempts", arangodb.CollectionTypeDocument)
	if err != nil && !shared.IsConflict(err) {
		log.Fatalf("Failed to create verification_attempts collection: %v", err)
	}

	return db
}
