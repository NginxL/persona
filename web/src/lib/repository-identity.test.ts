import { expect, it } from "vitest";
import fixtures from "../../../spec/fixtures/repository-remotes.json";
import { parseRepository, remoteHost } from "./repository-identity";

// Prefix + URL keeps Git transport users separate from email-like fixture text.
for (const { url, provider, identity, prefix = "" } of fixtures.valid) {
  it(`normalizes ${url}`, () => {
    expect(parseRepository(prefix + url, provider)).toEqual(identity);
    expect(parseRepository(identity.canonical_url, identity.provider)).toEqual(identity);
  });
}
for (const { url, provider, prefix = "" } of fixtures.invalid) {
  it(`refuses ${JSON.stringify(url)}`, () => expect(parseRepository(prefix + url, provider)).toBeUndefined());
}

// The host an extension's source is matched by: read as the parser reads an address, and only one without credentials.
it("reads an address's host the way the parser does, for matching a source's declared hosts", () => {
  expect(remoteHost("https://Code.Acme.test/acme-code/acme/reviews/3")).toBe("code.acme.test");
  expect(remoteHost("git@" + "code.acme.test:acme-code/acme.git")).toBe("code.acme.test");
  expect(remoteHost("ssh://git@" + "code.acme.test:2222/acme-code/acme.git")).toBe("code.acme.test");
  expect(remoteHost("http://code.acme.test:8080/acme-code/acme")).toBe("code.acme.test");
  for (const remote of ["https://user" + "@code.acme.test/acme-code/acme", "https://code.acme.test/acme-code/acme?private_token=x", "ssh://deploy" + "@code.acme.test/acme",
    "https://[::1]/acme", "ftp://code.acme.test/acme", "code.acme.test/acme", "https://code.acme.test/acme code", ""]) {
    expect(remoteHost(remote), remote).toBeUndefined();
  }
});
