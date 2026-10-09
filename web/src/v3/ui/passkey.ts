/**
 * WebAuthn helpers for the server's go-webauthn options (base64url fields inside `publicKey`).
 * The browser API needs ArrayBuffers; the server wants the standard JSON serialisation back.
 */
const toBytes = (b64url: string) => {
  const b64 = b64url.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(b64url.length / 4) * 4, "=");
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
};
const toB64url = (buf: ArrayBuffer | null) =>
  buf ? btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "") : null;

type AssertionOptions = {
  publicKey: Omit<PublicKeyCredentialRequestOptions, "challenge" | "allowCredentials"> & {
    challenge: string;
    allowCredentials?: { id: string; type: PublicKeyCredentialType; transports?: AuthenticatorTransport[] }[];
  };
};

export const passkeysSupported = () => typeof window !== "undefined" && !!window.PublicKeyCredential && !!navigator.credentials;

/** Runs the browser's passkey prompt for a server-issued assertion and returns the JSON the server verifies. */
export async function getAssertion(options: AssertionOptions, signal?: AbortSignal) {
  const pk = options.publicKey;
  const cred = (await navigator.credentials.get({
    signal,
    publicKey: {
      ...pk,
      challenge: toBytes(pk.challenge),
      allowCredentials: pk.allowCredentials?.map((c) => ({ ...c, id: toBytes(c.id) })),
    },
  })) as PublicKeyCredential | null;
  if (!cred) throw new DOMException("No credential", "NotAllowedError");
  const r = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      authenticatorData: toB64url(r.authenticatorData),
      clientDataJSON: toB64url(r.clientDataJSON),
      signature: toB64url(r.signature),
      userHandle: toB64url(r.userHandle),
    },
  };
}
