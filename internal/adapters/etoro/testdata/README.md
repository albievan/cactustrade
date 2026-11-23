# eToro fixtures

Synthetic responses shaped exactly like eToro's OpenAPI spec (v1.419.0) examples and
schemas. No real account data, keys or ids. Replace with **sanitised** recordings from the
demo account in phase 2.7 (strip ids that could identify the account, never commit keys).

The execution fixtures (`open`, `lookup_pending`, `lookup_rejected`, `lookup_partial`,
`lookup_short`, `close`, `close_order`) are synthetic too, built from the v1.419.0 schemas;
step 4.17 replaces them with sanitised demo recordings.
