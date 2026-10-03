"use strict";

const { doubleCsrf } = require("csrf-csrf");
const {
  resolveSessionCookieOptions,
  resolveCsrfCookieName,
} = require("../../auth/session-cookie-options.js");

const sessionCookieOptions = resolveSessionCookieOptions(process.env);
const csrfCookieName = resolveCsrfCookieName(
  process.env,
  sessionCookieOptions,
);
const expressSessionCookie = sessionCookieOptions.secure
  ? { ...sessionCookieOptions, httpOnly: true, secure: true }
  : { ...sessionCookieOptions, httpOnly: true, secure: false };

const { generateToken: generateCsrfToken, doubleCsrfProtection } = doubleCsrf({
  getSecret: () => {
    if (!process.env.SESSION_SECRET) {
      throw new Error("SESSION_SECRET environment variable is required");
    }
    return process.env.SESSION_SECRET;
  },
  cookieName: csrfCookieName,
  cookieOptions: sessionCookieOptions.secure
    ? { ...expressSessionCookie, httpOnly: true, path: "/", secure: true }
    : { ...expressSessionCookie, httpOnly: true, path: "/", secure: false },
  getTokenFromRequest: (req) => req.headers["x-csrf-token"],
});

// A simple exempt middleware wrapper for WhatBreaks MVP
const csrfExempt = (req, res, next) => {
  if ((process.env.NODE_ENV || "").trim().toLowerCase() === "test") {
    return next();
  }
  return doubleCsrfProtection(req, res, next);
};

module.exports = {
  generateCsrfToken,
  csrfExempt,
};
