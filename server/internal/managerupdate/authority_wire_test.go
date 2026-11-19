package managerupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

func TestAuthorityWireReadsPersistedIdentityWithoutMigrationOrWrites(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Microsecond)
	values := [][]byte{[]byte("persisted-main"), []byte("original-machine"), []byte("42"),
		[]byte(at.Format("2006-01-02 15:04:05.999999-07:00")), []byte(at.Format("2006-01-02 15:04:05.999999-07:00")),
		[]byte("object"), []byte("string"), []byte("string")}
	assertAuthorityWire(t, values, "persisted-main", "original-machine", false, false)
}

func TestAuthorityWireRejectsAbsentSettingsWithoutExplicitEmptyRow(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Microsecond)
	values := [][]byte{nil, nil, nil, nil, []byte(at.Format("2006-01-02 15:04:05.999999-07:00")), nil, nil, nil}
	assertAuthorityWire(t, values, "", "", false, true)
}

func TestAuthorityWireRejectsMalformedExistingSettings(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Microsecond)
	stamp := []byte(at.Format("2006-01-02 15:04:05.999999-07:00"))
	for name, values := range map[string][][]byte{
		"row":       {nil, nil, []byte("42"), stamp, stamp, []byte("array"), nil, nil},
		"main type": {[]byte("[]"), []byte("machine"), []byte("42"), stamp, stamp, []byte("object"), []byte("array"), []byte("string")},
	} {
		t.Run(name, func(t *testing.T) { assertAuthorityWire(t, values, "", "", false, true) })
	}
}

// This is a disposable PostgreSQL wire fixture, not db.Open and not a connection
// to any configured product database. It rejects anything except one SELECT.
func assertAuthorityWire(t *testing.T, values [][]byte, wantMain, wantMachine string, wantUnassigned, wantErr bool) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	finished := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			finished <- err
			return
		}
		defer connection.Close()
		if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			finished <- err
			return
		}
		wire := pgproto3.NewBackend(connection, connection)
		startup, err := wire.ReceiveStartupMessage()
		if err != nil {
			finished <- err
			return
		}
		message, ok := startup.(*pgproto3.StartupMessage)
		if !ok || message.Parameters["default_transaction_read_only"] != "on" {
			finished <- fmt.Errorf("read-only startup missing")
			return
		}
		wire.Send(&pgproto3.AuthenticationOk{})
		wire.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "16.0"})
		wire.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
		wire.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
		wire.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if err := wire.Flush(); err != nil {
			finished <- err
			return
		}
		next, err := wire.Receive()
		if err != nil {
			finished <- err
			return
		}
		query, ok := next.(*pgproto3.Query)
		if !ok || !strings.HasPrefix(query.String, "SELECT s.value->>'main', s.value->>'machine_id', s.xmin::text") ||
			!strings.Contains(query.String, "LEFT JOIN public.settings AS s ON s.key = 'assistant'") {
			finished <- fmt.Errorf("unexpected authority SQL message: %#v", next)
			return
		}
		fields := make([]pgproto3.FieldDescription, 8)
		for i := range fields {
			fields[i] = pgproto3.FieldDescription{Name: []byte(fmt.Sprint(i)), DataTypeOID: 25, DataTypeSize: -1}
		}
		fields[3].DataTypeOID = 1184
		fields[4].DataTypeOID = 1184
		wire.Send(&pgproto3.RowDescription{Fields: fields})
		wire.Send(&pgproto3.DataRow{Values: values})
		wire.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		wire.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if err := wire.Flush(); err != nil {
			finished <- err
			return
		}
		next, err = wire.Receive()
		if err != nil {
			finished <- err
			return
		}
		if _, ok := next.(*pgproto3.Terminate); !ok {
			finished <- fmt.Errorf("extra SQL or mutation after SELECT")
			return
		}
		finished <- nil
	}()
	c, p := fixture(t)
	c.FileCheck = func(string, int) error { return nil }
	config := filepath.Join(p.Dir, "manager.json")
	data, err := json.Marshal(map[string]string{"id": p.ManagerID, "database_url": "postgres://fixture@" + listener.Addr().String() + "/fixture?sslmode=disable"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	p.ConfigSHA256, err = hashFile(config)
	if err != nil {
		t.Fatal(err)
	}
	p = identityOnly(p)
	before := tree(t, c.Home)
	result, readErr := c.ReadAuthority(context.Background(), p)
	// A read that never connected must fail the test, not leave the fake in Accept and the package at go test's timeout.
	_ = listener.Close()
	if err := <-finished; err != nil {
		t.Fatalf("fake Postgres: %v (ReadAuthority: %v)", err, readErr)
	}
	if wantErr {
		if readErr == nil {
			t.Fatalf("malformed settings were accepted as authority: %+v", result)
		}
	} else if readErr != nil || result.Main != wantMain || result.MachineID != wantMachine || result.Unassigned != wantUnassigned ||
		result.ManagerID != p.ManagerID || len(result.Revision) != 64 || result.ObservedAt.IsZero() {
		t.Fatalf("persisted authority: %+v %v", result, readErr)
	}
	if !reflect.DeepEqual(before, tree(t, c.Home)) {
		t.Fatal("authority read wrote product state")
	}
}
