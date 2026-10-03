const { logger } = require("../utils/logger");
const { pool } = require("../../database/database");

const router = require("express").Router();

router.get("/", (req, res) => res.send("API running"));

router.get("/health", async (req, res) => {
  try {
    await pool.query("SELECT 1");
    res.json({
      status: "healthy",
      timestamp: new Date().toISOString(),
      uptime: process.uptime(),
      environment: process.env.NODE_ENV,
    });
  } catch (err) {
    logger.error("Health check failed", { error: err.message });
    res.status(503).json({
      status: "unhealthy",
      timestamp: new Date().toISOString(),
      error: err.message,
    });
  }
});

module.exports = router;
