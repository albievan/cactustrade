import { Link } from "react-router-dom";

// NeedAccount explains why an account page is empty: the user has no eToro account yet
// (none of their own, none shared with them).
export function NeedAccount() {
  return (
    <div className="card border-warning"><div className="card-body">
      <h2 className="h5"><i className="bi bi-wallet2 me-2" />No eToro account yet</h2>
      <p className="mb-3">
        The watchlist, themes, risk limits and reviews belong to an eToro account. You have none: add your own (demo keys
        are fine), or ask the owner of an account to share it with you.
      </p>
      <Link className="btn btn-success" to="/accounts"><i className="bi bi-plus-lg me-1" />Add an eToro account</Link>
    </div></div>
  );
}
