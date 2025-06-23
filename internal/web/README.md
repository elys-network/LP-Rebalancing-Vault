# Web Module

The web module provides a comprehensive HTTP API and dashboard for monitoring the AVM (Autonomous Vault Manager) system. The web server starts automatically alongside the main AVM application.

## Features

### Dashboard
- **Real-time Vault Summary**: Current vault value, liquid USDC, active positions
- **Performance Metrics**: Total returns, gas fees, slippage, allocation efficiency
- **Recent Cycles**: Table view of recent rebalancing cycles with key metrics
- **Scoring Parameters**: Current configuration parameters for pool selection and scoring
- **Auto-refresh**: Dashboard updates every 30 seconds automatically

### API Endpoints

#### Health & Status
- `GET /api/health` - Server health check

#### Cycle Data
- `GET /api/cycles` - Get recent cycles (supports `?limit=N` parameter, max 100)
- `GET /api/cycles/{id}` - Get specific cycle by ID
- `GET /api/cycles/latest` - Get the most recent cycle

#### Analytics
- `GET /api/vault/summary` - High-level vault statistics
- `GET /api/performance` - Aggregated performance metrics
- `GET /api/scoring-parameters` - Current scoring parameters configuration

#### Dashboard
- `GET /` or `GET /dashboard` - Interactive web dashboard

## Usage

### Integrated with AVM

The web server starts automatically when you run the main AVM application:

```bash
# The web server will start on port 8080 by default
go run cmd/avm/main.go

# Or specify a custom port
WEB_PORT=3000 go run cmd/avm/main.go
```

### Environment Variables

The web server uses the same database configuration as the main AVM system:
- `DB_HOST` - Database host
- `DB_PORT` - Database port  
- `DB_USER` - Database username
- `DB_PASSWORD` - Database password
- `DB_NAME` - Database name
- `DB_SSLMODE` - SSL mode for database connection
- `WEB_PORT` - Web server port (optional, defaults to 8080)

### Accessing the Dashboard

Once the AVM starts, visit `http://localhost:8080` (or your configured port) to view the interactive dashboard.

## Integration Details

The web server runs in a separate goroutine alongside the main AVM cycle loop:
- Starts immediately after database initialization
- Runs concurrently with vault management operations
- Provides real-time access to cycle data as it's being generated
- Continues running for the lifetime of the AVM application

## API Response Formats

### Cycle Data
```json
{
  "cycles": [
    {
      "snapshot_id": 1,
      "cycle_number": 1,
      "timestamp": "2024-01-01T12:00:00Z",
      "initial_vault_value_usd": 100000.0,
      "final_vault_value_usd": 101000.0,
      "net_return_usd": 1000.0,
      "total_gas_fee_usd": 50.0,
      "allocation_efficiency_percent": 95.5,
      "transaction_hashes": ["0x123..."]
    }
  ],
  "count": 1,
  "limit": 20
}
```

### Vault Summary
```json
{
  "total_value": 101000.0,
  "liquid_usdc": 5000.0,
  "position_count": 4,
  "total_cycles": 10,
  "last_updated": "2024-01-01T12:00:00Z"
}
```

### Performance Metrics
```json
{
  "total_return": 5000.0,
  "total_gas_fees": 500.0,
  "total_slippage": 100.0,
  "avg_allocation_efficiency": 94.2,
  "total_cycles": 10,
  "successful_cycles": 8
}
```