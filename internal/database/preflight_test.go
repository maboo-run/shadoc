package database

import (
	"reflect"
	"testing"
)

func TestPreflightDumpArgumentsAreSchemaOnly(t *testing.T) {
	mysql, err := PreflightDumpArguments(MySQL, []string{"--defaults-extra-file=/tmp/credentials", "--", "app"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--defaults-extra-file=/tmp/credentials", "--no-data", "--", "app"}; !reflect.DeepEqual(mysql, want) {
		t.Fatalf("mysql args=%v want=%v", mysql, want)
	}

	postgres, err := PreflightDumpArguments(PostgreSQL, []string{"--format=custom", "--dbname", "app"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--format=custom", "--schema-only", "--dbname", "app"}; !reflect.DeepEqual(postgres, want) {
		t.Fatalf("postgres args=%v want=%v", postgres, want)
	}
}

func TestPreflightDumpArgumentsRejectMalformedMySQLCommand(t *testing.T) {
	if _, err := PreflightDumpArguments(MySQL, []string{"app"}); err == nil {
		t.Fatal("expected missing database delimiter error")
	}
}
