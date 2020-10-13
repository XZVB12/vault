package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/vault/sdk/database/newdbplugin"
	dbtesting "github.com/hashicorp/vault/sdk/database/newdbplugin/testing"
	"github.com/hashicorp/vault/sdk/helper/dbtxn"

	mssqlhelper "github.com/hashicorp/vault/helper/testhelpers/mssql"
)

func TestInitialize(t *testing.T) {
	cleanup, connURL := mssqlhelper.PrepareMSSQLTestContainer(t)
	defer cleanup()

	type testCase struct {
		req newdbplugin.InitializeRequest
	}

	tests := map[string]testCase{
		"happy path": {
			req: newdbplugin.InitializeRequest{
				Config: map[string]interface{}{
					"connection_url": connURL,
				},
				VerifyConnection: true,
			},
		},
		"max_open_connections set": {
			newdbplugin.InitializeRequest{
				Config: map[string]interface{}{
					"connection_url":       connURL,
					"max_open_connections": "5",
				},
				VerifyConnection: true,
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			db := new()
			dbtesting.AssertInitialize(t, db, test.req)
			defer dbtesting.AssertClose(t, db)

			if !db.Initialized {
				t.Fatal("Database should be initialized")
			}
		})
	}
}

func TestNewUser(t *testing.T) {
	cleanup, connURL := mssqlhelper.PrepareMSSQLTestContainer(t)
	defer cleanup()

	type testCase struct {
		req           newdbplugin.NewUserRequest
		usernameRegex string
		expectErr     bool
		assertUser    func(t testing.TB, connURL, username, password string)
	}

	tests := map[string]testCase{
		"no creation statements": {
			req: newdbplugin.NewUserRequest{
				UsernameConfig: newdbplugin.UsernameMetadata{
					DisplayName: "test",
					RoleName:    "test",
				},
				Statements: newdbplugin.Statements{},
				Password:   "AG4qagho-dsvZ",
				Expiration: time.Now().Add(1 * time.Second),
			},
			usernameRegex: "^$",
			expectErr:     true,
			assertUser:    assertCredsDoNotExist,
		},
		"with creation statements": {
			req: newdbplugin.NewUserRequest{
				UsernameConfig: newdbplugin.UsernameMetadata{
					DisplayName: "test",
					RoleName:    "test",
				},
				Statements: newdbplugin.Statements{
					Commands: []string{testMSSQLRole},
				},
				Password:   "AG4qagho-dsvZ",
				Expiration: time.Now().Add(1 * time.Second),
			},
			usernameRegex: "^v-test-test-[a-zA-Z0-9]{20}-[0-9]{10}$",
			expectErr:     false,
			assertUser:    assertCredsExist,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			usernameRe, err := regexp.Compile(test.usernameRegex)
			if err != nil {
				t.Fatalf("failed to compile username regex %q: %s", test.usernameRegex, err)
			}

			initReq := newdbplugin.InitializeRequest{
				Config: map[string]interface{}{
					"connection_url": connURL,
				},
				VerifyConnection: true,
			}

			db := new()
			dbtesting.AssertInitialize(t, db, initReq)
			defer dbtesting.AssertClose(t, db)

			createResp, err := db.NewUser(context.Background(), test.req)
			if test.expectErr && err == nil {
				t.Fatalf("err expected, got nil")
			}
			if !test.expectErr && err != nil {
				t.Fatalf("no error expected, got: %s", err)
			}

			if !usernameRe.MatchString(createResp.Username) {
				t.Fatalf("Generated username %q did not match regex %q", createResp.Username, test.usernameRegex)
			}

			// Protect against future fields that aren't specified
			expectedResp := newdbplugin.NewUserResponse{
				Username: createResp.Username,
			}
			if !reflect.DeepEqual(createResp, expectedResp) {
				t.Fatalf("Fields missing from expected response: Actual: %#v", createResp)
			}

			test.assertUser(t, connURL, createResp.Username, test.req.Password)
		})
	}
}

func TestUpdateUser_password(t *testing.T) {
	type testCase struct {
		req              newdbplugin.UpdateUserRequest
		expectErr        bool
		expectedPassword string
	}

	dbUser := "vaultuser"
	initPassword := "p4$sw0rd"

	tests := map[string]testCase{
		"missing password": {
			req: newdbplugin.UpdateUserRequest{
				Username: dbUser,
				Password: &newdbplugin.ChangePassword{
					NewPassword: "",
					Statements:  newdbplugin.Statements{},
				},
			},
			expectErr:        true,
			expectedPassword: initPassword,
		},
		"empty rotation statements": {
			req: newdbplugin.UpdateUserRequest{
				Username: dbUser,
				Password: &newdbplugin.ChangePassword{
					NewPassword: "N90gkKLy8$angf",
					Statements:  newdbplugin.Statements{},
				},
			},
			expectErr:        false,
			expectedPassword: "N90gkKLy8$angf",
		},
		"username rotation": {
			req: newdbplugin.UpdateUserRequest{
				Username: dbUser,
				Password: &newdbplugin.ChangePassword{
					NewPassword: "N90gkKLy8$angf",
					Statements: newdbplugin.Statements{
						Commands: []string{
							"ALTER LOGIN [{{username}}] WITH PASSWORD = '{{password}}'",
						},
					},
				},
			},
			expectErr:        false,
			expectedPassword: "N90gkKLy8$angf",
		},
		"bad statements": {
			req: newdbplugin.UpdateUserRequest{
				Username: dbUser,
				Password: &newdbplugin.ChangePassword{
					NewPassword: "N90gkKLy8$angf",
					Statements: newdbplugin.Statements{
						Commands: []string{
							"ahosh98asjdffs",
						},
					},
				},
			},
			expectErr:        true,
			expectedPassword: initPassword,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cleanup, connURL := mssqlhelper.PrepareMSSQLTestContainer(t)
			defer cleanup()

			initReq := newdbplugin.InitializeRequest{
				Config: map[string]interface{}{
					"connection_url": connURL,
				},
				VerifyConnection: true,
			}

			db := new()
			dbtesting.AssertInitialize(t, db, initReq)
			defer dbtesting.AssertClose(t, db)

			createTestMSSQLUser(t, connURL, dbUser, initPassword, testMSSQLLogin)

			assertCredsExist(t, connURL, dbUser, initPassword)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			updateResp, err := db.UpdateUser(ctx, test.req)
			if test.expectErr && err == nil {
				t.Fatalf("err expected, got nil")
			}
			if !test.expectErr && err != nil {
				t.Fatalf("no error expected, got: %s", err)
			}

			// Protect against future fields that aren't specified
			expectedResp := newdbplugin.UpdateUserResponse{}
			if !reflect.DeepEqual(updateResp, expectedResp) {
				t.Fatalf("Fields missing from expected response: Actual: %#v", updateResp)
			}

			assertCredsExist(t, connURL, dbUser, test.expectedPassword)
		})
	}
}

func TestDeleteUser(t *testing.T) {
	cleanup, connURL := mssqlhelper.PrepareMSSQLTestContainer(t)
	defer cleanup()

	dbUser := "vaultuser"
	initPassword := "p4$sw0rd"

	initReq := newdbplugin.InitializeRequest{
		Config: map[string]interface{}{
			"connection_url": connURL,
		},
		VerifyConnection: true,
	}

	db := new()
	dbtesting.AssertInitialize(t, db, initReq)
	defer dbtesting.AssertClose(t, db)

	createTestMSSQLUser(t, connURL, dbUser, initPassword, testMSSQLLogin)

	assertCredsExist(t, connURL, dbUser, initPassword)

	deleteReq := newdbplugin.DeleteUserRequest{
		Username: dbUser,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deleteResp, err := db.DeleteUser(ctx, deleteReq)
	if err != nil {
		t.Fatalf("Failed to delete user: %s", err)
	}

	// Protect against future fields that aren't specified
	expectedResp := newdbplugin.DeleteUserResponse{}
	if !reflect.DeepEqual(deleteResp, expectedResp) {
		t.Fatalf("Fields missing from expected response: Actual: %#v", deleteResp)
	}

	assertCredsDoNotExist(t, connURL, dbUser, initPassword)
}

func assertCredsExist(t testing.TB, connURL, username, password string) {
	t.Helper()
	err := testCredsExist(connURL, username, password)
	if err != nil {
		t.Fatalf("Unable to log in as %q: %s", username, err)
	}
}

func assertCredsDoNotExist(t testing.TB, connURL, username, password string) {
	t.Helper()
	err := testCredsExist(connURL, username, password)
	if err == nil {
		t.Fatalf("Able to log in when it shouldn't")
	}
}

func testCredsExist(connURL, username, password string) error {
	// Log in with the new creds
	parts := strings.Split(connURL, "@")
	connURL = fmt.Sprintf("sqlserver://%s:%s@%s", username, password, parts[1])
	db, err := sql.Open("mssql", connURL)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Ping()
}

func createTestMSSQLUser(t *testing.T, connURL string, username, password, query string) {
	db, err := sql.Open("mssql", connURL)
	defer db.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Start a transaction
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	m := map[string]string{
		"name":     username,
		"password": password,
	}
	if err := dbtxn.ExecuteTxQuery(ctx, tx, m, query); err != nil {
		t.Fatal(err)
	}
	// Commit the transaction
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

const testMSSQLRole = `
CREATE LOGIN [{{name}}] WITH PASSWORD = '{{password}}';
CREATE USER [{{name}}] FOR LOGIN [{{name}}];
GRANT SELECT, INSERT, UPDATE, DELETE ON SCHEMA::dbo TO [{{name}}];`

const testMSSQLDrop = `
DROP USER [{{name}}];
DROP LOGIN [{{name}}];
`

const testMSSQLLogin = `
CREATE LOGIN [{{name}}] WITH PASSWORD = '{{password}}';
`
