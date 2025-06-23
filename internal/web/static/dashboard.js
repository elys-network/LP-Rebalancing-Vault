async function fetchAPI(endpoint) {
    try {
        const response = await fetch('/api' + endpoint);
        if (!response.ok) {
            throw new Error('HTTP error! status: ' + response.status);
        }
        return await response.json();
    } catch (error) {
        console.error('API call failed:', error);
        throw error;
    }
}

async function loadVaultSummary() {
    try {
        const data = await fetchAPI('/vault/summary');
        const html = `
            <div class="grid">
                <div class="metric">
                    <div class="metric-value">$${data.total_value ? data.total_value.toLocaleString() : 'N/A'}</div>
                    <div class="metric-label">Total Vault Value</div>
                </div>
                <div class="metric">
                    <div class="metric-value">$${data.liquid_usdc ? data.liquid_usdc.toLocaleString() : 'N/A'}</div>
                    <div class="metric-label">Liquid USDC</div>
                </div>
                <div class="metric">
                    <div class="metric-value">${data.position_count || 0}</div>
                    <div class="metric-label">Active Positions</div>
                </div>
                <div class="metric">
                    <div class="metric-value">${data.total_cycles || 0}</div>
                    <div class="metric-label">Total Cycles</div>
                </div>
            </div>
        `;
        document.getElementById('vault-summary').innerHTML = html;
    } catch (error) {
        document.getElementById('vault-summary').innerHTML = '<div class="error">Failed to load vault summary</div>';
    }
}

async function loadPerformanceMetrics() {
    try {
        const data = await fetchAPI('/performance');
        const html = `
            <div class="grid">
                <div class="metric">
                    <div class="metric-value status-error">
                        $${data.total_slippage ? Math.abs(data.total_slippage).toLocaleString() : '0'}
                    </div>
                    <div class="metric-label">Total Slippage Cost</div>
                </div>
                <div class="metric">
                    <div class="metric-value status-error">$${data.total_gas_fees ? data.total_gas_fees.toLocaleString() : '0'}</div>
                    <div class="metric-label">Total Gas Fees</div>
                </div>
                <div class="metric">
                    <div class="metric-value status-good">${data.avg_allocation_efficiency ? data.avg_allocation_efficiency.toFixed(1) : '0'}%</div>
                    <div class="metric-label">Avg Allocation Efficiency</div>
                </div>
                <div class="metric">
                    <div class="metric-value status-error">
                        $${(data.total_slippage && data.total_gas_fees) ? (Math.abs(data.total_slippage) + data.total_gas_fees).toLocaleString() : '0'}
                    </div>
                    <div class="metric-label">Total Trading Costs</div>
                </div>
            </div>
        `;
        document.getElementById('performance-metrics').innerHTML = html;
    } catch (error) {
        document.getElementById('performance-metrics').innerHTML = '<div class="error">Failed to load performance metrics</div>';
    }
}

async function loadRecentCycles() {
    try {
        const data = await fetchAPI('/cycles?limit=10');
        if (!data.cycles || data.cycles.length === 0) {
            document.getElementById('recent-cycles').innerHTML = '<p>No cycles found</p>';
            return;
        }

        let html = `
            <table>
                <thead>
                    <tr>
                        <th>Cycle #</th>
                        <th>Timestamp</th>
                        <th>Vault Value</th>
                        <th>Trading Cost</th>
                        <th>Gas Fees</th>
                        <th>Efficiency</th>
                        <th>Actions</th>
                    </tr>
                </thead>
                <tbody>
        `;

        data.cycles.forEach(cycle => {
            const timestamp = new Date(cycle.timestamp).toLocaleString();
            const tradingCost = Math.abs(cycle.net_return_usd || 0);
            const tradingCostClass = tradingCost > 0 ? 'status-error' : 'status-good';
            const cycleId = `cycle-${cycle.cycle_number}`;
            
            html += `
                <tr>
                    <td>${cycle.cycle_number}</td>
                    <td>${timestamp}</td>
                    <td>$${cycle.final_vault_value_usd ? cycle.final_vault_value_usd.toLocaleString() : 'N/A'}</td>
                    <td class="${tradingCostClass}">
                        $${tradingCost.toFixed(2)}
                    </td>
                    <td>$${cycle.total_gas_fee_usd ? cycle.total_gas_fee_usd.toFixed(2) : '0'}</td>
                    <td>${cycle.allocation_efficiency_percent ? cycle.allocation_efficiency_percent.toFixed(1) : '0'}%</td>
                    <td>
                        <button onclick="toggleActions('${cycleId}')" class="action-toggle-btn">
                            ${cycle.action_receipts ? cycle.action_receipts.length : 0} Actions
                        </button>
                    </td>
                </tr>
            `;
            
            // Add expandable action details row
            if (cycle.action_receipts && cycle.action_receipts.length > 0) {
                html += `
                    <tr id="${cycleId}-details" class="action-details-row" style="display: none;">
                        <td colspan="7">
                            <div class="action-details">
                                <h4>Action Details:</h4>
                                <table class="action-table">
                                    <thead>
                                        <tr>
                                            <th>Type</th>
                                            <th>Pool ID</th>
                                            <th>Amount (USD)</th>
                                            <th>Status</th>
                                            <th>Timestamp</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                `;
                
                cycle.action_receipts.forEach(action => {
                    const actionType = action.original_sub_action ? action.original_sub_action.type : 'Unknown';
                    const poolId = getPoolIdFromAction(action.original_sub_action);
                    const actualAmountUSD = action.actual_amount_usd || 0;
                    const actionTimestamp = new Date(action.timestamp).toLocaleString();
                    const statusClass = action.success ? 'status-good' : 'status-error';
                    
                    html += `
                        <tr>
                            <td>${formatActionType(actionType)}</td>
                            <td>${poolId || 'N/A'}</td>
                            <td>$${actualAmountUSD.toLocaleString()}</td>
                            <td class="${statusClass}">${action.success ? '✓ Success' : '✗ Failed'}</td>
                            <td>${actionTimestamp}</td>
                        </tr>
                    `;
                });
                
                html += `
                                    </tbody>
                                </table>
                            </div>
                        </td>
                    </tr>
                `;
            }
        });

        html += '</tbody></table>';
        document.getElementById('recent-cycles').innerHTML = html;
    } catch (error) {
        document.getElementById('recent-cycles').innerHTML = '<div class="error">Failed to load recent cycles</div>';
    }
}

function toggleActions(cycleId) {
    const detailsRow = document.getElementById(cycleId + '-details');
    if (detailsRow) {
        detailsRow.style.display = detailsRow.style.display === 'none' ? 'table-row' : 'none';
    }
}

function getPoolIdFromAction(subAction) {
    if (!subAction) return null;
    return subAction.pool_id_to_deposit || subAction.pool_id_to_withdraw || subAction.pool_id_for_swap || null;
}

function formatActionType(actionType) {
    switch(actionType) {
        case 'DEPOSIT_LP': return '💰 Deposit';
        case 'WITHDRAW_LP': return '💸 Withdraw';
        case 'SWAP': return '🔄 Swap';
        default: return actionType;
    }
}

async function loadScoringParameters() {
    try {
        const data = await fetchAPI('/scoring-parameters');
        if (!data.parameters) {
            document.getElementById('scoring-parameters').innerHTML = '<p>No scoring parameters found</p>';
            return;
        }

        const params = data.parameters;
        const html = `
            <div class="grid">
                <div>
                    <h4>Pool Selection</h4>
                    <p><strong>Max Pools:</strong> ${params.max_pools}</p>
                    <p><strong>Min TVL Threshold:</strong> $${params.min_tvl_threshold ? params.min_tvl_threshold.toLocaleString() : 'N/A'}</p>
                    <p><strong>Pool Maturity Days:</strong> ${params.pool_maturity_days}</p>
                </div>
                <div>
                    <h4>Allocation Limits</h4>
                    <p><strong>Min Allocation:</strong> ${(params.min_allocation * 100).toFixed(2)}%</p>
                    <p><strong>Max Allocation:</strong> ${(params.max_allocation * 100).toFixed(2)}%</p>
                    <p><strong>Rebalance Threshold:</strong> ${params.rebalance_threshold_amount ? params.rebalance_threshold_amount.toFixed(2) : 'N/A'}%</p>
                    <p><strong>Max Withdrawal Per Cycle:</strong> ${params.max_rebalance_percent_per_cycle ? params.max_rebalance_percent_per_cycle.toFixed(2) : 'N/A'}%</p>
                </div>
                <div>
                    <h4>Risk Management</h4>
                    <p><strong>Smart Shield Slippage:</strong> ${params.smart_shield_slippage_percent ? params.smart_shield_slippage_percent.toFixed(2) : 'N/A'}%</p>
                    <p><strong>Normal Pool Slippage:</strong> ${params.normal_pool_slippage_percent ? params.normal_pool_slippage_percent.toFixed(2) : 'N/A'}%</p>
                    <p><strong>Min Liquid USDC Buffer:</strong> $${params.min_liquid_usdc_buffer ? params.min_liquid_usdc_buffer.toLocaleString() : 'N/A'}</p>
                </div>
                <div>
                    <h4>Scoring Weights</h4>
                    <p><strong>APR Coefficient:</strong> ${params.apr_coefficient}</p>
                    <p><strong>Volume Coefficient:</strong> ${params.trading_volume_coefficient}</p>
                    <p><strong>TVL Coefficient:</strong> ${params.tvl_coefficient}</p>
                </div>
            </div>
        `;
        document.getElementById('scoring-parameters').innerHTML = html;
    } catch (error) {
        document.getElementById('scoring-parameters').innerHTML = '<div class="error">Failed to load scoring parameters</div>';
    }
}

async function loadDashboard() {
    await Promise.all([
        loadVaultSummary(),
        loadPerformanceMetrics(),
        loadRecentCycles(),
        loadScoringParameters()
    ]);
}

// Load dashboard on page load
document.addEventListener('DOMContentLoaded', loadDashboard);

// Auto-refresh every 30 seconds
setInterval(loadDashboard, 30000); 