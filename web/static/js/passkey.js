// Passkeys (WebAuthn) for sign-in, re-authentication and registration.
//
// Every [data-passkey] block holds a start button, an error line and the two
// endpoints of its ceremony: "begin" answers with the options for
// navigator.credentials, "finish" takes the authenticator's answer. The
// server speaks base64url; the browser API wants ArrayBuffers.
(function () {
  "use strict";

  function toBuffer(text) {
    var raw = atob(text.replace(/-/g, "+").replace(/_/g, "/"));
    var bytes = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
    return bytes.buffer;
  }

  function toBase64url(buffer) {
    var bytes = new Uint8Array(buffer);
    var raw = "";
    for (var i = 0; i < bytes.length; i++) raw += String.fromCharCode(bytes[i]);
    return btoa(raw).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  // The CSRF token is the one htmx sends, from hx-headers on <body>.
  function post(url, body) {
    var headers = { "Content-Type": "application/json" };
    var csrf = document.body.getAttribute("hx-headers");
    if (csrf) Object.assign(headers, JSON.parse(csrf));
    return fetch(url, { method: "POST", headers: headers, body: JSON.stringify(body), credentials: "same-origin" }).then(function (res) {
      if ((res.headers.get("Content-Type") || "").indexOf("application/json") !== 0) {
        // Sent to another page, for example to confirm the password first.
        if (res.redirected) {
          location.assign(res.url);
          return new Promise(function () {});
        }
        throw new Error("The server did not answer as expected. Reload the page and try again.");
      }
      return res.json().then(function (data) {
        if (!res.ok) throw new Error(data.error || "The request failed.");
        return data;
      });
    });
  }

  function decodeOptions(publicKey) {
    publicKey.challenge = toBuffer(publicKey.challenge);
    if (publicKey.user) publicKey.user.id = toBuffer(publicKey.user.id);
    (publicKey.excludeCredentials || publicKey.allowCredentials || []).forEach(function (c) {
      c.id = toBuffer(c.id);
    });
    return publicKey;
  }

  function encodeCredential(cred) {
    var r = cred.response;
    var response = { clientDataJSON: toBase64url(r.clientDataJSON) };
    if (r.attestationObject) {
      response.attestationObject = toBase64url(r.attestationObject);
      response.transports = r.getTransports ? r.getTransports() : [];
    } else {
      response.authenticatorData = toBase64url(r.authenticatorData);
      response.signature = toBase64url(r.signature);
      if (r.userHandle) response.userHandle = toBase64url(r.userHandle);
    }
    return {
      id: cred.id,
      rawId: toBase64url(cred.rawId),
      type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      clientExtensionResults: {},
      response: response
    };
  }

  function explain(err) {
    switch (err && err.name) {
      case "NotAllowedError":
        return "The passkey prompt was closed or timed out. Try again.";
      case "InvalidStateError":
        return "This device already has a passkey for this account.";
      case "SecurityError":
        return "This address cannot use passkeys.";
    }
    return (err && err.message) || "Something went wrong. Try again.";
  }

  function start(block) {
    var creating = block.getAttribute("data-passkey") === "create";
    var label = block.querySelector("[data-passkey-label]");
    return post(block.getAttribute("data-begin"), {
      next: block.getAttribute("data-next") || "",
      label: label ? label.value : ""
    })
      .then(function (options) {
        var request = { publicKey: decodeOptions(options.publicKey) };
        return creating ? navigator.credentials.create(request) : navigator.credentials.get(request);
      })
      .then(function (cred) {
        return post(block.getAttribute("data-finish"), encodeCredential(cred));
      })
      .then(function (done) {
        location.assign(done.redirect);
      });
  }

  Array.prototype.forEach.call(document.querySelectorAll("[data-passkey]"), function (block) {
    var button = block.querySelector("[data-passkey-start]");
    var error = block.querySelector("[data-passkey-error]");
    button.addEventListener("click", function () {
      error.hidden = true;
      if (!window.PublicKeyCredential) {
        error.textContent = "This browser does not support passkeys.";
        error.hidden = false;
        return;
      }
      button.disabled = true;
      start(block).catch(function (err) {
        error.textContent = explain(err);
        error.hidden = false;
        button.disabled = false;
      });
    });
  });
})();
