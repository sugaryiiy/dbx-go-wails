package database

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type mongoDriver struct {
	client *mongo.Client
	config Config
}

func init() { nativeOpeners["mongodb"] = openMongo }

type mongoDialer func(context.Context, string, string) (net.Conn, error)

func (d mongoDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d(ctx, network, address)
}

// MongoDB is deliberately a single explicit endpoint. Discovery and SRV could
// connect to servers outside the selected SSH route and are not inferred.
func mongoClientOptions(c Config) (*options.ClientOptions, error) {
	if strings.TrimSpace(c.Host) == "" || strings.ContainsAny(c.Host, "/,@?# ") {
		return nil, errors.New("MongoDB requires one hostname or IP address")
	}
	if c.ConnectionString != "" {
		return nil, unsupported("MongoDB connection URI, replica sets and SRV discovery")
	}
	if c.DriverProfile != "" && c.DriverProfile != "native" && c.DriverProfile != "mongodb" {
		return nil, unsupported("MongoDB legacy or custom driver profile")
	}
	params, err := url.ParseQuery(strings.TrimPrefix(c.URLParams, "?"))
	if err != nil {
		return nil, errors.New("invalid MongoDB URL parameters")
	}
	for key, values := range params {
		if len(values) != 1 {
			return nil, fmt.Errorf("duplicate MongoDB parameter %s", key)
		}
		switch key {
		case "authSource", "authMechanism":
		case "directConnection":
			if values[0] != "true" {
				return nil, unsupported("MongoDB topology discovery")
			}
		default:
			return nil, unsupported("MongoDB URL parameter " + key)
		}
	}
	authSource := params.Get("authSource")
	if authSource == "" {
		authSource = str(c.Raw, "auth_source")
	}
	if authSource == "" {
		authSource = c.Database
	}
	if authSource == "" {
		authSource = "admin"
	}
	mechanism := params.Get("authMechanism")
	if mechanism != "" && mechanism != "SCRAM-SHA-1" && mechanism != "SCRAM-SHA-256" {
		return nil, unsupported("MongoDB authentication mechanism " + mechanism)
	}
	port := c.Port
	if port == 0 {
		port = 27017
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid MongoDB port")
	}
	timeout := c.ConnectTimeout
	if timeout <= 0 {
		timeout = 15
	}
	if timeout > 300 {
		timeout = 300
	}
	tlsConfig, err := databaseTLSConfig(c)
	if err != nil {
		return nil, err
	}
	dial, err := networkDialer(c)
	if err != nil {
		return nil, err
	}
	clientOptions := options.Client().SetHosts([]string{net.JoinHostPort(strings.Trim(c.Host, "[]"), strconv.Itoa(port))}).
		SetDirect(true).SetRetryWrites(false).SetRetryReads(false).
		SetConnectTimeout(time.Duration(timeout) * time.Second).SetServerSelectionTimeout(time.Duration(timeout) * time.Second).
		SetMaxPoolSize(8).SetMaxConnIdleTime(5 * time.Minute).SetTLSConfig(tlsConfig).SetDialer(mongoDialer(dial)).SetAppName("DBX-Go-Wails")
	if c.Username != "" {
		clientOptions.SetAuth(options.Credential{AuthSource: authSource, AuthMechanism: mechanism, Username: c.Username, Password: c.Password, PasswordSet: true})
	} else if c.Password != "" || mechanism != "" {
		return nil, errors.New("MongoDB username is required for password authentication")
	}
	return clientOptions, nil
}

func openMongo(ctx context.Context, c Config) (nativeDriver, error) {
	opts, err := mongoClientOptions(c)
	if err != nil {
		return nil, redact(err, c)
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, redact(err, c)
	}
	d := &mongoDriver{client: client, config: c}
	if err := d.Ping(ctx); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func (d *mongoDriver) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.client.Disconnect(ctx)
}
func (d *mongoDriver) Ping(ctx context.Context) error {
	return redact(d.client.Ping(ctx, readpref.Primary()), d.config)
}
func (d *mongoDriver) Info(ctx context.Context) (map[string]any, error) {
	var info bson.M
	if err := d.client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info); err != nil {
		return nil, redact(err, d.config)
	}
	return map[string]any{"productName": "MongoDB", "productVersion": info["version"], "driverName": "Go MongoDB driver", "currentDatabase": d.config.Database, "unquotedIdentifierCase": "mixed", "quotedIdentifierCase": "mixed"}, nil
}

func mongoNamespace(name string, database bool) error {
	if name == "" || strings.ContainsRune(name, 0) {
		return errors.New("MongoDB database and collection names must be nonempty and contain no NUL")
	}
	if database && (len(name) > 63 || strings.ContainsAny(name, `/\\. "$*<>:|?`)) {
		return errors.New("invalid MongoDB database name")
	}
	if !database && (strings.HasPrefix(name, "$") || len(name) > 255) {
		return errors.New("invalid MongoDB collection name")
	}
	return nil
}

func mongoIsWrite(path string) bool {
	switch path {
	case "/api/document-store/insert-document", "/api/document-store/update-document", "/api/document-store/delete-document",
		"/api/mongo/create-database", "/api/mongo/drop-database", "/api/mongo/create-collection", "/api/mongo/drop-collection", "/api/mongo/rename-collection", "/api/mongo/clone-collection",
		"/api/mongo/create-index", "/api/mongo/drop-indexes", "/api/mongo/create-user", "/api/mongo/insert-documents", "/api/mongo/update-documents", "/api/mongo/delete-documents", "/api/mongo/replace-document",
		"/api/mongo/find-one-and-update", "/api/mongo/find-one-and-replace", "/api/mongo/find-one-and-delete", "/api/mongo/bulk-write":
		return true
	}
	return false
}

func (d *mongoDriver) Handle(ctx context.Context, path string, p map[string]any, locked bool) (result any, err error) {
	defer func() { err = redact(err, d.config) }()
	if mongoIsWrite(path) && locked {
		return nil, errors.New("connection is read-only or production-locked; unlock writes before modifying MongoDB")
	}
	if path == "/api/document-store/list-databases" || path == "/api/schema/databases" {
		names, e := d.client.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
		sort.Strings(names)
		return names, e
	}
	database := str(p, "database")
	if database == "" {
		database = d.config.Database
	}
	if database == "" {
		database = "admin"
	}
	if err = mongoNamespace(database, true); err != nil {
		return nil, err
	}
	db := d.client.Database(database)
	if path == "/api/mongo/server-version" {
		info, e := d.Info(ctx)
		if e != nil {
			return nil, e
		}
		return info["productVersion"], nil
	}
	if path == "/api/document-store/list-collections" {
		cursor, e := db.ListCollections(ctx, bson.D{}, options.ListCollections().SetNameOnly(true))
		if e != nil {
			return nil, e
		}
		defer cursor.Close(ctx)
		items := []map[string]any{}
		for cursor.Next(ctx) {
			var doc bson.M
			if e = cursor.Decode(&doc); e != nil {
				return nil, e
			}
			items = append(items, map[string]any{"name": doc["name"], "id": doc["name"], "kind": doc["type"]})
			if len(items) > 10000 {
				return nil, errors.New("MongoDB collection listing exceeds 10000 entries")
			}
		}
		sort.Slice(items, func(i, j int) bool { return fmt.Sprint(items[i]["name"]) < fmt.Sprint(items[j]["name"]) })
		return items, cursor.Err()
	}
	if path == "/api/mongo/create-database" {
		return nil, db.CreateCollection(ctx, "dbx_init")
	}
	if path == "/api/mongo/drop-database" {
		return nil, db.Drop(ctx)
	}
	if path == "/api/mongo/run-command" {
		return d.runCommand(ctx, db, p, locked)
	}
	collection := str(p, "collection", "table")
	if err = mongoNamespace(collection, false); err != nil {
		return nil, err
	}
	col := db.Collection(collection)
	switch path {
	case "/api/schema/columns", "/api/schema/indexes":
		return d.schema(ctx, col, path)
	case "/api/mongo/create-collection":
		return d.createCollection(ctx, db, collection, p)
	case "/api/mongo/drop-collection":
		return nil, col.Drop(ctx)
	case "/api/mongo/rename-collection":
		name := str(p, "newName")
		if err = mongoNamespace(name, false); err != nil {
			return nil, err
		}
		return nil, d.client.Database("admin").RunCommand(ctx, bson.D{{Key: "renameCollection", Value: database + "." + collection}, {Key: "to", Value: database + "." + name}, {Key: "dropTarget", Value: false}}).Err()
	case "/api/document-store/find-documents", "/api/mongo/find-one", "/api/mongo/count-documents", "/api/document-store/count-documents", "/api/mongo/aggregate-documents", "/api/mongo/distinct", "/api/mongo/explain-find":
		return d.readDocuments(ctx, db, col, path, p)
	case "/api/document-store/insert-document", "/api/document-store/update-document", "/api/document-store/delete-document", "/api/mongo/insert-documents", "/api/mongo/update-documents", "/api/mongo/replace-document", "/api/mongo/delete-documents", "/api/mongo/find-one-and-update", "/api/mongo/find-one-and-replace", "/api/mongo/find-one-and-delete":
		return d.writeDocuments(ctx, db, col, path, p)
	case "/api/mongo/list-index-specs", "/api/mongo/create-index", "/api/mongo/drop-indexes", "/api/mongo/collection-stats":
		return d.handleIndexes(ctx, db, col, path, p)
	}
	return nil, unsupported(path)
}
