package v3passkey

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// A software authenticator constructs standard browser response JSON. CBOR is
// encoded by the maintained WebAuthn library; the test never parses CBOR itself.
type testAuthenticator struct {
	private      *ecdsa.PrivateKey
	credentialID []byte
	rpID         string
	origin       string
}

func newTestAuthenticator(t *testing.T, rpID, origin string) testAuthenticator {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := make([]byte, 32)
	if _, err = rand.Read(credentialID); err != nil {
		t.Fatal(err)
	}
	return testAuthenticator{private: private, credentialID: credentialID, rpID: rpID, origin: origin}
}

func (a testAuthenticator) authenticatorData(flags byte, count uint32, attested []byte) []byte {
	rpHash := sha256.Sum256([]byte(a.rpID))
	data := append([]byte{}, rpHash[:]...)
	data = append(data, flags)
	data = binary.BigEndian.AppendUint32(data, count)
	return append(data, attested...)
}

func (a testAuthenticator) clientData(t *testing.T, ceremony, challenge string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"type": ceremony, "challenge": challenge, "origin": a.origin, "crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (a testAuthenticator) register(t *testing.T, challenge string, flags byte) []byte {
	t.Helper()
	public := a.private.PublicKey
	coseKey, err := webauthncbor.Marshal(map[int64]any{
		1: int64(webauthncose.EllipticKey), 3: int64(webauthncose.AlgES256),
		-1: int64(webauthncose.P256),
		-2: public.X.FillBytes(make([]byte, 32)), -3: public.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	attested := make([]byte, 16) // unspecified AAGUID; attestation is "none"
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(a.credentialID)))
	attested = append(attested, a.credentialID...)
	attested = append(attested, coseKey...)
	authData := a.authenticatorData(flags|byte(protocol.FlagAttestedCredentialData), 0, attested)
	attestation, err := webauthncbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": authData,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(a.credentialID)
	body, err := json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key",
		"response": map[string]any{
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestation),
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(a.clientData(t, "webauthn.create", challenge)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (a testAuthenticator) assert(t *testing.T, challenge string, flags byte, count uint32, signingKey *ecdsa.PrivateKey) []byte {
	t.Helper()
	authData := a.authenticatorData(flags, count, nil)
	clientData := a.clientData(t, "webauthn.get", challenge)
	clientHash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authData...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	if signingKey == nil {
		signingKey = a.private
	}
	signature, err := ecdsa.SignASN1(rand.Reader, signingKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(a.credentialID)
	body, err := json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key",
		"response": map[string]any{
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"signature":         base64.RawURLEncoding.EncodeToString(signature),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (f *passkeyFixture) challenge(t *testing.T, id string) string {
	t.Helper()
	var challenge string
	if err := f.pool.QueryRow(t.Context(), `SELECT session_data->>'challenge' FROM v3_passkey_challenges WHERE id=$1`, id).Scan(&challenge); err != nil {
		t.Fatal(err)
	}
	if challenge == "" {
		t.Fatal("empty durable WebAuthn challenge")
	}
	return challenge
}

func TestPasskeyES256RegistrationAndAssertion(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	a := newTestAuthenticator(t, "aiops.example.invalid", "https://aiops.example.invalid")
	registration, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, registration.ID,
		a.register(t, f.challenge(t, registration.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified)), "Test passkey"); err != nil {
		t.Fatalf("valid ES256 registration: %v", err)
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, registration.ID,
		a.register(t, f.challenge(t, registration.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified)), "Replay"); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("registration replay: %v", err)
	}
	assertion, err := f.svc.BeginAssertion(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	response := a.assert(t, f.challenge(t, assertion.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified), 1, nil)
	if _, err = f.svc.FinishAssertion(ctx, f.actor, assertion.ID, response); err != nil {
		t.Fatalf("valid ES256 assertion: %v", err)
	}
	if _, err = f.svc.FinishAssertion(ctx, f.actor, assertion.ID, response); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("assertion replay: %v", err)
	}
	// A fresh challenge must not accept the same or a lower nonzero counter.
	rollback, err := f.svc.BeginAssertion(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishAssertion(ctx, f.actor, rollback.ID,
		a.assert(t, f.challenge(t, rollback.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified), 1, nil)); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("counter rollback accepted: %v", err)
	}
	var revoked, cloneWarning bool
	if err = f.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL,clone_warning
	 FROM v3_passkey_credentials WHERE credential_id=$1`, a.credentialID).Scan(&revoked, &cloneWarning); err != nil {
		t.Fatal(err)
	}
	if !revoked || !cloneWarning {
		t.Fatalf("counter rollback did not quarantine credential: revoked=%t clone_warning=%t", revoked, cloneWarning)
	}
	if _, err = f.svc.BeginAssertion(ctx, f.actor); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("quarantined credential remained usable: %v", err)
	}
}

func TestPasskeyRejectsWrongOriginUVAndSignature(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	a := newTestAuthenticator(t, "aiops.example.invalid", "https://aiops.example.invalid")
	registration, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	wrongOrigin := a
	wrongOrigin.origin = "https://attacker.example.invalid"
	if _, err = f.svc.FinishRegistration(ctx, f.actor, registration.ID,
		wrongOrigin.register(t, f.challenge(t, registration.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified)), "Bad origin"); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("wrong registration origin accepted: %v", err)
	}
	registration, err = f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, registration.ID,
		a.register(t, f.challenge(t, registration.ID), byte(protocol.FlagUserPresent)), "No UV"); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("registration without UV accepted: %v", err)
	}
	registration, err = f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, registration.ID,
		a.register(t, f.challenge(t, registration.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified)), "Valid"); err != nil {
		t.Fatal(err)
	}
	assertion, err := f.svc.BeginAssertion(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishAssertion(ctx, f.actor, assertion.ID,
		a.assert(t, f.challenge(t, assertion.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified), 1, other)); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("forged assertion signature accepted: %v", err)
	}
	assertion, err = f.svc.BeginAssertion(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishAssertion(ctx, f.actor, assertion.ID,
		a.assert(t, f.challenge(t, assertion.ID), byte(protocol.FlagUserPresent), 1, nil)); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("assertion without UV accepted: %v", err)
	}
	assertion, err = f.svc.BeginAssertion(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishAssertion(ctx, f.actor, assertion.ID,
		wrongOrigin.assert(t, f.challenge(t, assertion.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified), 1, nil)); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("wrong assertion origin accepted: %v", err)
	}
}

func TestPasskeyDeletionRevokesCredential(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	a := newTestAuthenticator(t, "aiops.example.invalid", "https://aiops.example.invalid")
	registration, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, registration.ID,
		a.register(t, f.challenge(t, registration.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified)), "Device"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.BeginDeletion(ctx, f.actor, []byte("unknown")); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("unknown deletion target: %v", err)
	}
	deletion, err := f.svc.BeginDeletion(ctx, f.actor, a.credentialID)
	if err != nil {
		t.Fatal(err)
	}
	response := a.assert(t, f.challenge(t, deletion.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified), 1, nil)
	if err = f.svc.FinishDeletion(ctx, f.actor, deletion.ID, response); err != nil {
		t.Fatalf("valid credential deletion: %v", err)
	}
	if err = f.svc.FinishDeletion(ctx, f.actor, deletion.ID, response); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("deletion replay: %v", err)
	}
	if _, err = f.svc.BeginAssertion(ctx, f.actor); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("revoked credential remained usable: %v", err)
	}
	var active, audited int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM v3_passkey_credentials WHERE revoked_at IS NULL`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM management_audit
	 WHERE actor_id=$1 AND action='passkey.delete.finish' AND outcome='success'`, f.actor.UserID).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if active != 0 || audited != 1 {
		t.Fatalf("deletion active=%d success audits=%d", active, audited)
	}
}

func TestPasskeyConcurrentRegistrationConsumesChallengeOnce(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	a := newTestAuthenticator(t, "aiops.example.invalid", "https://aiops.example.invalid")
	ceremony, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	response := a.register(t, f.challenge(t, ceremony.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified))
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, finishErr := f.svc.FinishRegistration(ctx, f.actor, ceremony.ID, response, "Device")
			results <- finishErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var successes, denied int
	for finishErr := range results {
		switch {
		case finishErr == nil:
			successes++
		case errors.Is(finishErr, ErrChallengeInvalid):
			denied++
		default:
			t.Fatalf("concurrent finish: %v", finishErr)
		}
	}
	if successes != 1 || denied != 1 {
		t.Fatalf("same challenge success=%d denied=%d", successes, denied)
	}
	var credentialCount, auditCount int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM v3_passkey_credentials WHERE credential_id=$1`, a.credentialID).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM management_audit
	 WHERE actor_id=$1 AND action='passkey.registration.finish' AND outcome='success'`, f.actor.UserID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if credentialCount != 1 || auditCount != 1 {
		t.Fatalf("same challenge credentials=%d success audits=%d", credentialCount, auditCount)
	}
}

func TestPasskeyRegistrationExcludesActiveCredential(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	a := newTestAuthenticator(t, "aiops.example.invalid", "https://aiops.example.invalid")
	first, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, first.ID,
		a.register(t, f.challenge(t, first.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified)), "Device"); err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	options, ok := second.Options.(*protocol.CredentialCreation)
	if !ok {
		t.Fatalf("registration options type %T", second.Options)
	}
	var found bool
	for _, descriptor := range options.Response.CredentialExcludeList {
		if bytes.Equal(descriptor.CredentialID, a.credentialID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("active credential absent from excludeCredentials: %d descriptors", len(options.Response.CredentialExcludeList))
	}
}

func TestPasskeyChallengeExpiresWhileWaitingForActorLock(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	a := newTestAuthenticator(t, "aiops.example.invalid", "https://aiops.example.invalid")
	ceremony, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	response := a.register(t, f.challenge(t, ceremony.ID), byte(protocol.FlagUserPresent|protocol.FlagUserVerified))
	lockTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(context.Background())
	if _, err = lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(752018992)); err != nil {
		t.Fatal(err)
	}
	var expiresAt time.Time
	if err = f.pool.QueryRow(ctx, `UPDATE v3_passkey_challenges
	 SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1 RETURNING expires_at`, ceremony.ID).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, finishErr := f.svc.FinishRegistration(ctx, f.actor, ceremony.ID, response, "Device")
		result <- finishErr
	}()
	// Observe the service blocked on the advisory lock before the deadline.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
		 WHERE datname=current_database() AND wait_event_type='Lock'
		 AND query LIKE 'SELECT pg_advisory_xact_lock%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("registration finish did not wait on account advisory lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if delay := time.Until(expiresAt.Add(100 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	if err = lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrChallengeInvalid) && !errors.Is(err, ErrFreshLoginRequired) {
		t.Fatalf("expired while queued challenge accepted: %v", err)
	}
	var credentialCount int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM v3_passkey_credentials`).Scan(&credentialCount); err != nil || credentialCount != 0 {
		t.Fatalf("queued expired challenge stored %d credentials: %v", credentialCount, err)
	}
}
