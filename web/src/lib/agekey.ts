// An age key pair made in the browser (R48.9): WebCrypto X25519 and age's Bech32 encoding,
// no dependency. The identity (private key, AGE-SECRET-KEY-1…) is shown once for the admin
// to save and never leaves the browser; only the recipient (public key, age1…) is sent to
// cactusTrade. The same encoding as `age-keygen` and filippo.io/age.

const CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l";
const GENERATOR = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3];

function polymod(values: number[]): number {
  let chk = 1;
  for (const v of values) {
    const top = chk >>> 25;
    chk = (((chk & 0x1ffffff) << 5) ^ v) >>> 0;
    for (let i = 0; i < 5; i++) if ((top >>> i) & 1) chk = (chk ^ GENERATOR[i]) >>> 0;
  }
  return chk;
}

// bech32Encode encodes bytes under hrp (BIP 173 checksum, no length limit, as age does);
// the result is lower case.
export function bech32Encode(hrp: string, data: Uint8Array): string {
  const h = hrp.toLowerCase();
  const words: number[] = [];
  let acc = 0;
  let bits = 0;
  for (const b of data) {
    acc = ((acc << 8) | b) & 0xffff;
    bits += 8;
    while (bits >= 5) {
      bits -= 5;
      words.push((acc >>> bits) & 31);
    }
  }
  if (bits > 0) words.push((acc << (5 - bits)) & 31);
  const expanded = [...[...h].map((c) => c.charCodeAt(0) >>> 5), 0, ...[...h].map((c) => c.charCodeAt(0) & 31)];
  const mod = polymod([...expanded, ...words, 0, 0, 0, 0, 0, 0]) ^ 1;
  const checksum = Array.from({ length: 6 }, (_, i) => (mod >>> (5 * (5 - i))) & 31);
  return h + "1" + [...words, ...checksum].map((w) => CHARSET[w]).join("");
}

function fromBase64Url(s: string): Uint8Array {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

// AgeKeyPair: identity = the private key (keep it secret, off this machine); recipient = the
// public key cactusTrade encrypts backups to.
export interface AgeKeyPair { identity: string; recipient: string }

function fromJwk(jwk: JsonWebKey): AgeKeyPair {
  if (!jwk.d || !jwk.x) throw new Error("the browser did not export the X25519 key");
  return {
    identity: bech32Encode("AGE-SECRET-KEY-", fromBase64Url(jwk.d)).toUpperCase(),
    recipient: bech32Encode("age", fromBase64Url(jwk.x)),
  };
}

function subtleOf(subtle?: SubtleCrypto): SubtleCrypto {
  const s = subtle ?? globalThis.crypto?.subtle;
  if (!s) throw new Error("this browser has no Web Crypto (open the page over HTTPS)");
  return s;
}

// x25519Supported reports whether this browser can make an X25519 key pair.
export async function x25519Supported(subtle?: SubtleCrypto): Promise<boolean> {
  try {
    await subtleOf(subtle).generateKey({ name: "X25519" }, true, ["deriveBits"]);
    return true;
  } catch {
    return false;
  }
}

// generateAgeKeyPair makes a new random age key pair in the browser.
export async function generateAgeKeyPair(subtle?: SubtleCrypto): Promise<AgeKeyPair> {
  const s = subtleOf(subtle);
  const kp = (await s.generateKey({ name: "X25519" }, true, ["deriveBits"])) as CryptoKeyPair;
  return fromJwk(await s.exportKey("jwk", kp.privateKey));
}

// PKCS #8 header of a raw X25519 private key (RFC 8410).
const PKCS8_X25519 = [0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e, 0x04, 0x22, 0x04, 0x20];

// ageKeyPairFromScalar is the key pair of a given 32-byte X25519 scalar (known-answer tests).
export async function ageKeyPairFromScalar(scalar: Uint8Array, subtle?: SubtleCrypto): Promise<AgeKeyPair> {
  if (scalar.length !== 32) throw new Error("an X25519 private key is 32 bytes");
  const s = subtleOf(subtle);
  const der = new Uint8Array(48);
  der.set(PKCS8_X25519);
  der.set(scalar, 16);
  const key = await s.importKey("pkcs8", der, { name: "X25519" }, true, ["deriveBits"]);
  return fromJwk(await s.exportKey("jwk", key));
}

// AGE_KEYGEN_COMMAND makes a key pair without the browser.
export const AGE_KEYGEN_COMMAND = "age-keygen -o cactustrade-backup.agekey";
