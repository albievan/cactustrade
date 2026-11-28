import { webcrypto } from "node:crypto";
import { describe, expect, it } from "vitest";
import { ageKeyPairFromScalar, bech32Encode, generateAgeKeyPair, x25519Supported } from "./agekey";

const subtle = webcrypto.subtle as unknown as SubtleCrypto;

// The known-answer vector of internal/adapters/backup TestAgeKeyVector: the scalar
// 0x01 0x02 … 0x20, checked there with filippo.io/age. A test value, never a real key.
const VECTOR = {
  identity: "AGE-SECRET-KEY-1QYPQXPQ9QCRSSZG2PVXQ6RS0ZQG3YYC5Z5TPWXQERGD3C8G7RUSQGPQYEE",
  recipient: "age1q73he0q5yzfu3d64msd3p6rvksnrwjk3d2598mgtmlqt9wrdr37q2vrn72",
};

describe("agekey", () => {
  it("matches age's encoding for a fixed scalar", async () => {
    const scalar = Uint8Array.from({ length: 32 }, (_, i) => i + 1);
    expect(await ageKeyPairFromScalar(scalar, subtle)).toEqual(VECTOR);
  });

  it("encodes Bech32 like BIP 173", () => {
    // BIP 173's valid test string "a12uel5l" is the empty payload under hrp "a".
    expect(bech32Encode("a", new Uint8Array())).toBe("a12uel5l");
    expect(bech32Encode("AGE", new Uint8Array([0]))).toMatch(/^age1qq/);
  });

  it("makes random key pairs of the right shape", async () => {
    expect(await x25519Supported(subtle)).toBe(true);
    const a = await generateAgeKeyPair(subtle);
    const b = await generateAgeKeyPair(subtle);
    expect(a.identity).toMatch(/^AGE-SECRET-KEY-1[0-9A-Z]{58}$/);
    expect(a.recipient).toMatch(/^age1[0-9a-z]{58}$/);
    expect(a.identity).not.toBe(b.identity);
  });

  it("refuses a scalar of the wrong length", async () => {
    await expect(ageKeyPairFromScalar(new Uint8Array(31), subtle)).rejects.toThrow("32 bytes");
  });

  it("says when X25519 is missing", async () => {
    const none = { generateKey: () => Promise.reject(new Error("NotSupportedError")) } as unknown as SubtleCrypto;
    expect(await x25519Supported(none)).toBe(false);
  });
});
