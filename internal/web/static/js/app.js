// Traveler Dashboard - Main Application Logic

class TravelerApp {
    constructor() {
        this.signals = [];
        this.excluded = new Set();
        this.capital = 50000;
        this.currentSignal = null;
        this.market = 'us'; // 'us' or 'kr'
        this.positionsRefreshTimer = null;
        this.activeTab = 'scanner';

        this.initEventListeners();
        this.initTabs();
        this.initMarketToggle();

        // Auto-load last scan result or attach to running scan
        this.loadLastResult();
    }

    // ==================== TAB NAVIGATION ====================
    initTabs() {
        document.querySelectorAll('.tab-btn').forEach(btn => {
            btn.addEventListener('click', () => this.switchTab(btn.dataset.tab));
        });
    }

    // ==================== MARKET TOGGLE ====================
    initMarketToggle() {
        document.querySelectorAll('.market-btn').forEach(btn => {
            btn.addEventListener('click', () => this.switchMarket(btn.dataset.market));
        });
    }

    switchMarket(market) {
        if (market === this.market) return;
        this.market = market;

        // Update toggle buttons
        document.querySelectorAll('.market-btn').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.market === market);
            if (btn.dataset.market !== market) {
                btn.classList.add('text-gray-400');
            } else {
                btn.classList.remove('text-gray-400');
            }
        });

        // Update capital label and defaults
        const capitalLabel = document.querySelector('label[for="capitalInput"], label');
        const capitalInput = document.getElementById('capitalInput');
        const isSim = market.startsWith('sim-');
        if (market === 'kr' || market === 'sim-kr' || market === 'crypto') {
            if (capitalLabel) capitalLabel.textContent = market === 'crypto' ? 'Capital (₩):' : 'Capital:';
            if (capitalInput && parseFloat(capitalInput.value) < 10000) {
                capitalInput.value = 1000000;
                this.capital = 1000000;
            }
        } else {
            if (capitalLabel) capitalLabel.textContent = 'Capital ($):';
            if (capitalInput && parseFloat(capitalInput.value) >= 10000) {
                capitalInput.value = 200;
                this.capital = 200;
            }
        }

        // sim 마켓에서는 스캔 버튼 비활성화
        const scanBtn = document.getElementById('startScan');
        if (scanBtn) {
            scanBtn.disabled = isSim;
            scanBtn.style.opacity = isSim ? '0.4' : '1';
            scanBtn.title = isSim ? 'Scan runs automatically via daemon' : '';
        }

        // Reload data for active tab
        if (this.activeTab === 'positions') {
            this.loadPositionsData();
        } else if (this.activeTab === 'scanner') {
            this.loadLastResult(true);
        } else if (this.activeTab === 'history') {
            this.loadTradeHistory();
        }
    }

    isKR() {
        return this.market === 'kr' || this.market === 'sim-kr';
    }

    isCrypto() {
        return this.market === 'crypto';
    }

    // KRW currency (both KR stocks and crypto use Korean Won)
    isKRW() {
        return this.market === 'kr' || this.market === 'sim-kr' || this.market === 'crypto';
    }

    isSim() {
        return this.market === 'sim-us' || this.market === 'sim-kr';
    }

    // Returns market query string (e.g. '?market=kr', '&market=crypto', or '')
    marketQuery(sep = '?') {
        if (this.market === 'us') return '';
        return `${sep}market=${this.market}`;
    }

    switchTab(tab) {
        this.activeTab = tab;

        // Update tab buttons
        document.querySelectorAll('.tab-btn').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.tab === tab);
        });

        // Update panels
        document.getElementById('panelScanner').classList.toggle('hidden', tab !== 'scanner');
        document.getElementById('panelPositions').classList.toggle('hidden', tab !== 'positions');
        document.getElementById('panelHistory').classList.toggle('hidden', tab !== 'history');
        document.getElementById('panelStrategy').classList.toggle('hidden', tab !== 'strategy');

        // Load data for specific tabs
        if (tab === 'positions') {
            this.loadPositionsData();
            this.startPositionsRefresh();
        } else {
            this.stopPositionsRefresh();
        }

        if (tab === 'history') {
            this.loadTradeHistory();
        }
    }

    startPositionsRefresh() {
        this.stopPositionsRefresh();
        this.positionsRefreshTimer = setInterval(() => {
            this.loadPositionsData();
        }, 30000);
    }

    stopPositionsRefresh() {
        if (this.positionsRefreshTimer) {
            clearInterval(this.positionsRefreshTimer);
            this.positionsRefreshTimer = null;
        }
    }

    // ==================== POSITIONS TAB ====================
    async loadPositionsData() {
        try {
            const mq = this.marketQuery();
            const [posRes, balRes, ordRes] = await Promise.all([
                fetch('/api/positions' + mq).then(r => r.json()).catch(() => ({ positions: [] })),
                fetch('/api/balance' + mq).then(r => r.json()).catch(() => ({})),
                fetch('/api/orders' + mq).then(r => r.json()).catch(() => ({ orders: [] })),
            ]);

            this.renderAccountSummary(balRes, posRes.positions || []);
            this.renderPositionCards(posRes.positions || []);
            this.renderPendingOrders(ordRes.orders || []);

            // Update last refresh time
            const el = document.getElementById('lastRefresh');
            if (el) el.textContent = `(${new Date().toLocaleTimeString()})`;
        } catch (e) {
            console.error('Failed to load positions data:', e);
        }
    }

    renderAccountSummary(balance, positions) {
        const equity = balance.total_equity || 0;
        const cash = balance.cash_balance || 0;

        let totalInvested = 0;
        let totalPnL = 0;
        positions.forEach(p => {
            totalInvested += (p.avg_cost || 0) * (p.quantity || 0);
            totalPnL += p.unrealized_pnl || 0;
        });

        const fmt = (v) => this.formatMoney(v);
        document.getElementById('acctEquity').textContent = fmt(equity);
        document.getElementById('acctCash').textContent = fmt(cash);
        document.getElementById('acctInvested').textContent = fmt(totalInvested);

        const pnlEl = document.getElementById('acctPnL');
        const pnlSign = totalPnL >= 0 ? '+' : '';
        pnlEl.textContent = `${pnlSign}${fmt(totalPnL)}`;
        pnlEl.className = `text-2xl font-bold ${totalPnL > 0 ? 'pnl-positive' : totalPnL < 0 ? 'pnl-negative' : 'pnl-neutral'}`;

        document.getElementById('acctPositionCount').textContent = positions.length;
    }

    renderPositionCards(positions) {
        const container = document.getElementById('positionCards');
        const noPos = document.getElementById('noPositions');

        if (positions.length === 0) {
            container.innerHTML = '';
            noPos.classList.remove('hidden');
            return;
        }

        noPos.classList.add('hidden');
        container.innerHTML = positions.map(pos => this.createPositionCard(pos)).join('');

        // Add click handlers for chart view
        container.querySelectorAll('.position-card').forEach(card => {
            card.addEventListener('click', () => {
                const symbol = card.dataset.symbol;
                this.openPositionChart(symbol, positions.find(p => p.symbol === symbol));
            });
        });
    }

    createPositionCard(pos) {
        const symbol = pos.symbol || '';
        const name = pos.name || '';
        const displayName = name && name !== symbol ? `${symbol} ${name}` : symbol;
        const qty = pos.quantity || 0;
        const avgCost = pos.avg_cost || 0;
        const current = pos.current_price || avgCost;
        const pnl = pos.unrealized_pnl || 0;
        const pnlPct = pos.unrealized_pct || 0;
        const hasPlan = pos.has_plan || false;
        const fp = (v) => this.formatPrice(v);

        // Strategy badge
        const strategy = pos.strategy || '';
        const stratClass = strategy ? `strategy-${strategy}` : 'strategy-unknown';
        const stratLabel = strategy || 'no plan';

        // P&L display
        const pnlSign = pnl >= 0 ? '+' : '';
        const pnlClass = pnl > 0 ? 'pnl-positive' : pnl < 0 ? 'pnl-negative' : 'pnl-neutral';

        // Price level bar
        const priceLevelBar = hasPlan ? this.createPriceLevelBar(pos) : '';

        // Stop/Target info
        const stopTarget = hasPlan ? `
            <div class="grid grid-cols-2 gap-x-4 gap-y-1 text-xs mt-2">
                <div><span class="text-gray-500">Stop:</span> <span class="text-red-400">${fp(pos.stop_loss)}</span> <span class="text-gray-600">(${this.pctDiff(avgCost, pos.stop_loss)})</span></div>
                <div><span class="text-gray-500">T1:</span> <span class="text-green-400">${fp(pos.target1)}</span> <span class="text-gray-600">(${this.pctDiff(avgCost, pos.target1)})</span>${pos.target1_hit ? ' <span class="text-yellow-400">HIT</span>' : ''}</div>
                <div><span class="text-gray-500">T2:</span> <span class="text-green-400">${fp(pos.target2)}</span> <span class="text-gray-600">(${this.pctDiff(avgCost, pos.target2)})</span></div>
                ${pos.breakout_level > 0 ? `<div><span class="text-gray-500">Breakout:</span> <span class="text-orange-400">${fp(pos.breakout_level)}</span></div>` : ''}
            </div>
        ` : '<div class="text-xs text-gray-500 mt-2">No plan data - restart daemon to generate</div>';

        // Time progress
        const timeProgress = hasPlan && pos.max_hold_days > 0 ? this.createTimeProgress(pos) : '';

        // Invalidation warning
        const invalidationWarning = this.createInvalidationWarning(pos);

        return `
            <div class="position-card" data-symbol="${symbol}">
                <div class="flex items-center justify-between mb-2">
                    <div class="flex items-center gap-2">
                        <span class="text-lg font-bold text-white">${displayName}</span>
                        <span class="strategy-badge ${stratClass}">${stratLabel}</span>
                    </div>
                    <div class="text-right">
                        <span class="${pnlClass} font-semibold">${pnlSign}${this.formatMoney(Math.abs(pnl))}</span>
                        <span class="${pnlClass} text-sm ml-1">${pnlSign}${pnlPct.toFixed(1)}%</span>
                    </div>
                </div>
                <div class="text-sm text-gray-400">
                    Entry: ${fp(avgCost)} &nbsp; Current: ${fp(current)} &nbsp; Qty: ${qty}
                </div>
                ${priceLevelBar}
                ${stopTarget}
                ${timeProgress}
                ${invalidationWarning}
            </div>
        `;
    }

    createPriceLevelBar(pos) {
        const stop = pos.stop_loss || 0;
        const entry = pos.avg_cost || 0;
        const current = pos.current_price || entry;
        const t1 = pos.target1 || 0;
        const t2 = pos.target2 || 0;

        if (stop === 0 || t2 === 0) return '';

        const min = stop * 0.995;
        const max = t2 * 1.005;
        const range = max - min;
        if (range <= 0) return '';

        const pct = (val) => Math.max(0, Math.min(100, ((val - min) / range) * 100));

        const stopPct = pct(stop);
        const entryPct = pct(entry);
        const currentPct = pct(current);
        const t1Pct = pct(t1);
        const t2Pct = pct(t2);

        // Fill color based on current position
        let fillColor, fillLeft, fillRight;
        if (current >= entry) {
            fillColor = 'rgba(34, 197, 94, 0.3)';
            fillLeft = entryPct;
            fillRight = currentPct;
        } else {
            fillColor = 'rgba(239, 68, 68, 0.3)';
            fillLeft = currentPct;
            fillRight = entryPct;
        }

        return `
            <div class="price-level-bar">
                <div class="bar-fill" style="left:${fillLeft}%;width:${fillRight - fillLeft}%;background:${fillColor}"></div>
                <div class="bar-marker" style="left:${stopPct}%;background:#ef4444;" title="Stop $${stop.toFixed(2)}"></div>
                <div class="bar-marker" style="left:${entryPct}%;background:#3b82f6;" title="Entry $${entry.toFixed(2)}"></div>
                <div class="bar-marker" style="left:${currentPct}%;background:#ffffff;width:14px;height:14px;" title="Current $${current.toFixed(2)}"></div>
                <div class="bar-marker" style="left:${t1Pct}%;background:#22c55e;" title="T1 $${t1.toFixed(2)}"></div>
                <div class="bar-marker" style="left:${t2Pct}%;background:#22c55e;" title="T2 $${t2.toFixed(2)}"></div>
                <div class="bar-label" style="left:${stopPct}%">SL</div>
                <div class="bar-label" style="left:${entryPct}%">E</div>
                <div class="bar-label" style="left:${currentPct}%;color:#fff;font-weight:600">&#9660;</div>
                <div class="bar-label" style="left:${t1Pct}%">T1</div>
                <div class="bar-label" style="left:${t2Pct}%">T2</div>
            </div>
            <div style="height:16px"></div>
        `;
    }

    createTimeProgress(pos) {
        const held = pos.days_held || 0;
        const max = pos.max_hold_days || 7;
        const remaining = pos.days_remaining || 0;
        const pct = Math.min(100, (held / max) * 100);

        let fillClass = 'time-fill-safe';
        if (pct >= 80) fillClass = 'time-fill-danger';
        else if (pct >= 50) fillClass = 'time-fill-warning';

        return `
            <div class="mt-2">
                <div class="flex justify-between text-xs text-gray-500 mb-1">
                    <span>Day ${held}/${max}</span>
                    <span>${remaining > 0 ? remaining + ' remaining' : 'TIME UP'}</span>
                </div>
                <div class="time-progress">
                    <div class="time-fill ${fillClass}" style="width:${pct}%"></div>
                </div>
            </div>
        `;
    }

    createInvalidationWarning(pos) {
        if (!pos.has_plan) return '';

        // Pullback: consecutive days below MA20
        if (pos.strategy === 'pullback' && pos.consecutive_days_below > 0) {
            return `
                <div class="invalidation-warning">
                    <span>&#9888;</span>
                    <span>Below MA20: ${pos.consecutive_days_below}/2 days</span>
                </div>
            `;
        }

        // Time expired
        if (pos.days_remaining !== undefined && pos.days_remaining <= 0 && pos.max_hold_days > 0) {
            return `
                <div class="invalidation-warning" style="background:rgba(239,68,68,0.15);color:#ef4444">
                    <span>&#9888;</span>
                    <span>Time stop exceeded</span>
                </div>
            `;
        }

        return '';
    }

    renderPendingOrders(orders) {
        const section = document.getElementById('ordersSection');
        const tbody = document.getElementById('ordersTable');

        if (orders.length === 0) {
            section.classList.add('hidden');
            return;
        }

        section.classList.remove('hidden');
        tbody.innerHTML = orders.map(o => `
            <tr class="text-sm">
                <td class="px-4 py-3 font-semibold text-blue-400">${o.symbol}</td>
                <td class="px-4 py-3 ${o.side === 'BUY' ? 'text-green-400' : 'text-red-400'}">${o.side}</td>
                <td class="px-4 py-3">${o.quantity}</td>
                <td class="px-4 py-3">${o.filled_qty}</td>
                <td class="px-4 py-3">${this.formatPrice(o.price || 0)}</td>
                <td class="px-4 py-3"><span class="badge badge-hold">${o.status}</span></td>
            </tr>
        `).join('');
    }

    async openPositionChart(symbol, pos) {
        if (!symbol) return;
        try {
            const res = await fetch(`/api/stock/${symbol}`);
            if (!res.ok) return;
            const data = await res.json();

            const guide = pos && pos.has_plan ? {
                entry_price: pos.avg_cost,
                stop_loss: pos.stop_loss,
                target_1: pos.target1,
                target_2: pos.target2,
            } : null;

            // Reuse stock modal
            const displayLabel = pos && pos.name && pos.name !== symbol ? `${symbol} ${pos.name}` : symbol;
            document.getElementById('modalTitle').textContent = displayLabel;
            document.getElementById('modalEntry').textContent = pos ? this.formatPrice(pos.avg_cost) : '--';
            document.getElementById('modalStopLoss').textContent = pos && pos.stop_loss ? this.formatPrice(pos.stop_loss) : '--';
            document.getElementById('modalTarget1').textContent = pos && pos.target1 ? this.formatPrice(pos.target1) : '--';
            document.getElementById('modalTarget2').textContent = pos && pos.target2 ? this.formatPrice(pos.target2) : '--';
            document.getElementById('modalShares').value = pos ? pos.quantity : 0;
            document.getElementById('modalReason').textContent = pos && pos.strategy ? `Strategy: ${pos.strategy}` : 'No plan';

            if (data.candles && data.candles.length > 0) {
                renderChart('chartContainer', data.candles, guide);
            }

            // Calculate investment/risk for position
            if (pos) {
                const entry = pos.avg_cost || 0;
                const stop = pos.stop_loss || 0;
                const qty = pos.quantity || 0;
                const investment = qty * entry;
                const riskPerShare = stop > 0 ? entry - stop : 0;
                const riskAmount = qty * riskPerShare;
                const riskPct = this.capital > 0 ? (riskAmount / this.capital) * 100 : 0;
                document.getElementById('modalInvestment').textContent = this.formatMoney(investment);
                document.getElementById('modalRiskAmount').textContent = this.formatMoney(riskAmount);
                document.getElementById('modalRiskPct').textContent = `${riskPct.toFixed(2)}%`;
            }

            // Hide scanner-specific buttons
            document.getElementById('excludeBtn').classList.add('hidden');
            document.getElementById('applySharesBtn').classList.add('hidden');

            document.getElementById('stockModal').classList.remove('hidden');
        } catch (e) {
            console.error('Failed to load chart:', e);
        }
    }

    // ==================== HELPER ====================
    pctDiff(base, target) {
        if (!base || base === 0) return '0%';
        const diff = ((target - base) / base) * 100;
        const sign = diff >= 0 ? '+' : '';
        return `${sign}${diff.toFixed(1)}%`;
    }

    // ==================== SCANNER TAB (existing) ====================
    initEventListeners() {
        // File drop zone
        const dropZone = document.getElementById('dropZone');
        const fileInput = document.getElementById('fileInput');

        dropZone.addEventListener('click', () => fileInput.click());
        dropZone.addEventListener('dragover', (e) => {
            e.preventDefault();
            dropZone.classList.add('dragover');
        });
        dropZone.addEventListener('dragleave', () => {
            dropZone.classList.remove('dragover');
        });
        dropZone.addEventListener('drop', (e) => {
            e.preventDefault();
            dropZone.classList.remove('dragover');
            const files = e.dataTransfer.files;
            if (files.length > 0) {
                this.loadFile(files[0]);
            }
        });
        fileInput.addEventListener('change', (e) => {
            if (e.target.files.length > 0) {
                this.loadFile(e.target.files[0]);
            }
        });

        // Scan button
        document.getElementById('scanBtn').addEventListener('click', () => this.runScan());

        // Load last result button
        document.getElementById('loadLastBtn').addEventListener('click', () => this.loadLastResult());

        // Recalculate button
        document.getElementById('recalculateBtn').addEventListener('click', () => this.recalculate());

        // Capital input
        document.getElementById('capitalInput').addEventListener('change', (e) => {
            this.capital = parseFloat(e.target.value) || 50000;
        });

        // Stock modal
        document.getElementById('closeModal').addEventListener('click', () => this.hideStockModal());
        document.getElementById('excludeBtn').addEventListener('click', () => this.excludeCurrentStock());
        document.getElementById('applySharesBtn').addEventListener('click', () => this.applyShares());
        document.getElementById('modalShares').addEventListener('change', (e) => this.updateModalInvestment(e.target.value));

        // Close modals on escape
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                this.hideStockModal();
            }
        });

        // Click outside modal to close
        document.getElementById('stockModal').addEventListener('click', (e) => {
            if (e.target.id === 'stockModal') this.hideStockModal();
        });
    }

    async loadFile(file) {
        if (!file.name.endsWith('.json')) {
            alert('Please select a JSON file');
            return;
        }

        try {
            const text = await file.text();
            const data = JSON.parse(text);
            this.loadData(data);
        } catch (e) {
            console.error('Failed to parse JSON:', e);
            alert('Failed to parse JSON file: ' + e.message);
        }
    }

    loadData(data) {
        // Support both direct signal array and wrapped format
        if (data.signals) {
            this.signals = data.signals;
            if (data.capital) this.capital = data.capital;
        } else if (Array.isArray(data)) {
            this.signals = data;
        } else if (data.Signals) {
            // Handle Go JSON format (capitalized)
            this.signals = data.Signals;
            if (data.Capital) this.capital = data.Capital;
        }

        // Normalize signal format
        this.signals = this.signals.map(s => this.normalizeSignal(s));

        // Reset excluded
        this.excluded.clear();

        // Update UI
        document.getElementById('capitalInput').value = this.capital;
        this.recalculate();
        this.showUI();
    }

    normalizeSignal(signal) {
        // Handle both camelCase and PascalCase from Go JSON
        return {
            stock: signal.stock || signal.Stock || { symbol: 'Unknown', name: 'Unknown' },
            type: signal.type || signal.Type || 'BUY',
            strategy: signal.strategy || signal.Strategy || 'pullback',
            strength: signal.strength || signal.Strength || 0,
            probability: signal.probability || signal.Probability || 0,
            reason: signal.reason || signal.Reason || '',
            details: signal.details || signal.Details || {},
            guide: signal.guide || signal.Guide || null,
            candles: signal.candles || signal.Candles || [],
            fundamentals: signal.fundamentals || signal.Fundamentals || null
        };
    }

    showUI() {
        document.getElementById('dropZone').classList.add('hidden');
        document.getElementById('summaryCards').classList.remove('hidden');
        document.getElementById('controls').classList.remove('hidden');
        document.getElementById('signalsSection').classList.remove('hidden');
    }

    hideUI() {
        document.getElementById('dropZone').classList.remove('hidden');
        document.getElementById('summaryCards').classList.add('hidden');
        document.getElementById('controls').classList.add('hidden');
        document.getElementById('signalsSection').classList.add('hidden');
        document.getElementById('regimeBar').classList.add('hidden');
    }

    recalculate() {
        this.capital = parseFloat(document.getElementById('capitalInput').value) || this.capital;
        const minCap = this.isKRW() ? 100000 : 100;
        if (this.capital < minCap) {
            this.capital = minCap;
            document.getElementById('capitalInput').value = minCap;
        }

        const activeSignals = this.signals.filter(s =>
            !this.excluded.has(s.stock.symbol || s.stock.Symbol)
        );

        // Recalculate position sizing
        let totalInvest = 0;
        let totalRisk = 0;

        if (activeSignals.length > 0) {
            // Match server-side PositionSizer logic:
            // riskBudget = capital * riskPerTrade (NOT divided by positions)
            // maxPositionValue = capital * maxPositionPct
            let riskPct, maxPosPct;
            if (this.isCrypto()) {
                if (this.capital < 500000) {
                    riskPct = 3; maxPosPct = 0.30; // 50만 미만: 공격적
                } else if (this.capital < 5000000) {
                    riskPct = 2; maxPosPct = 0.25; // 500만 미만: 적극적
                } else {
                    riskPct = 1.5; maxPosPct = 0.20; // 500만 이상: 보수적
                }
            } else if (this.isKR()) {
                if (this.capital < 500000) {
                    riskPct = 3; maxPosPct = 0.40; // 50만 미만: 공격적
                } else if (this.capital < 5000000) {
                    riskPct = 2; maxPosPct = 0.30; // 500만 미만: 적극적
                } else {
                    riskPct = 1.5; maxPosPct = 0.25; // 500만 이상: 보수적
                }
            } else {
                if (this.capital < 500) {
                    riskPct = 2; maxPosPct = 0.30;
                } else {
                    riskPct = 1; maxPosPct = 0.20;
                }
            }
            const riskBudget = this.capital * (riskPct / 100); // per trade, not per position
            const maxPositionValue = this.capital * maxPosPct;
            const isCrypto = this.isCrypto();

            activeSignals.forEach(signal => {
                if (signal.guide) {
                    const g = signal.guide;
                    const entryPrice = g.entry_price || g.EntryPrice || 0;
                    const stopLoss = g.stop_loss || g.StopLoss || 0;
                    const riskPerShare = entryPrice - stopLoss;

                    if (riskPerShare > 0 && entryPrice > 0) {
                        let sharesByRisk, sharesByAllocation, shares;
                        if (isCrypto) {
                            // Crypto: fractional quantities allowed
                            sharesByRisk = riskBudget / riskPerShare;
                            sharesByAllocation = maxPositionValue / entryPrice;
                            shares = Math.min(sharesByRisk, sharesByAllocation);
                        } else {
                            sharesByRisk = Math.floor(riskBudget / riskPerShare);
                            sharesByAllocation = Math.floor(maxPositionValue / entryPrice);
                            shares = Math.min(sharesByRisk, sharesByAllocation);
                            if (shares < 1) shares = 1; // minimum 1 share
                        }

                        // Cap at maxPositionValue
                        if (shares * entryPrice > maxPositionValue) {
                            shares = isCrypto ? maxPositionValue / entryPrice : Math.floor(maxPositionValue / entryPrice);
                        }
                        if (!isCrypto && shares < 1) shares = 0;

                        // Update guide with new calculations
                        g.position_size = g.PositionSize = shares;
                        g.invest_amount = g.InvestAmount = shares * entryPrice;
                        g.risk_amount = g.RiskAmount = shares * riskPerShare;
                        g.risk_pct = g.RiskPct = (g.risk_amount / this.capital) * 100;
                        g.allocation_pct = g.AllocationPct = (g.invest_amount / this.capital) * 100;

                        totalInvest += g.invest_amount;
                        totalRisk += g.risk_amount;
                    }
                }
            });
        }

        // Update summary cards
        document.getElementById('totalCapital').textContent = this.formatMoney(this.capital);
        document.getElementById('totalInvested').textContent = this.formatMoney(totalInvest);
        document.getElementById('totalRisk').textContent = `${this.formatMoney(totalRisk)} (${(totalRisk / this.capital * 100).toFixed(2)}%)`;
        document.getElementById('cashRemaining').textContent = this.formatMoney(this.capital - totalInvest);

        // Update table
        this.renderTable(activeSignals);
    }

    renderTable(signals) {
        const tbody = document.getElementById('signalsTable');
        tbody.innerHTML = '';

        signals.forEach((signal, index) => {
            const symbol = signal.stock.symbol || signal.stock.Symbol || 'N/A';
            const guide = signal.guide || {};
            const entryPrice = guide.entry_price || guide.EntryPrice || 0;
            const shares = guide.position_size || guide.PositionSize || 0;
            const investAmount = guide.invest_amount || guide.InvestAmount || 0;
            const allocationPct = guide.allocation_pct || guide.AllocationPct || 0;
            const riskAmount = guide.risk_amount || guide.RiskAmount || 0;
            const probability = signal.probability || 0;
            const strategyName = signal.strategy || 'unknown';

            const row = document.createElement('tr');
            row.className = 'hover:bg-gray-750 cursor-pointer';
            const stockName = signal.stock.name || signal.stock.Name || '';
            const displaySym = stockName && stockName !== symbol ? `${symbol} <span class="text-gray-500 text-xs">${stockName}</span>` : symbol;

            const stratColors = {
                'pullback': 'bg-purple-600',
                'breakout': 'bg-orange-600',
                'mean-reversion': 'bg-teal-600',
                'volatility-breakout': 'bg-amber-600',
                'oversold': 'bg-cyan-600',
                'range-trading': 'bg-sky-600',
                'rsi-contrarian': 'bg-violet-600',
                'volume-spike': 'bg-orange-500'
            };
            // Parse regime from strategy name like "volatility-breakout(bull)"
            const regimeMatch = strategyName.match(/^(.+)\((bull|sideways|bear)\)$/);
            const baseStrategy = regimeMatch ? regimeMatch[1] : strategyName;
            const regime = regimeMatch ? regimeMatch[2] : (signal.details && signal.details.regime !== undefined ? ({1:'bull',0:'sideways','-1':'bear'}[signal.details.regime] || '') : '');
            const stratBg = stratColors[baseStrategy] || 'bg-gray-600';
            const regimeBadge = regime ? `<span class="${regime === 'bull' ? 'bg-green-700' : regime === 'bear' ? 'bg-red-700' : 'bg-yellow-700'} px-1.5 py-0.5 rounded text-xs ml-1">${regime.toUpperCase()}</span>` : '';

            // Fundamentals summary
            const fund = signal.fundamentals;
            let fundHTML = '<span class="text-gray-500">-</span>';
            if (fund) {
                const isKR = symbol.length === 6 && /^\d+$/.test(symbol);
                const de = fund.debtToEquity || fund.DebtToEquity || 0;
                const pm = ((fund.profitMargins || fund.ProfitMargins || 0) * 100).toFixed(1);
                const w52 = ((fund.fiftyTwoWeekChg || fund.FiftyTwoWeekChg || 0) * 100).toFixed(0);
                const mcap = fund.marketCap || fund.MarketCap || 0;
                const mcapStr = isKR
                    ? (mcap >= 1e12 ? (mcap/1e12).toFixed(1)+'T' : (mcap/1e8).toFixed(0)+'B')
                    : (mcap >= 1e9 ? (mcap/1e9).toFixed(1)+'B' : (mcap/1e6).toFixed(0)+'M');
                fundHTML = `<span class="text-xs">D/E:${de.toFixed(0)} PM:${pm}% 52W:${w52}%</span>`;
            }

            row.innerHTML = `
                <td class="px-4 py-3 text-gray-400">${index + 1}</td>
                <td class="px-4 py-3 font-semibold text-blue-400">${displaySym}</td>
                <td class="px-4 py-3"><span class="${stratBg} px-2 py-0.5 rounded text-xs font-medium">${baseStrategy}</span>${regimeBadge}</td>
                <td class="px-4 py-3">${this.formatPrice(entryPrice)}</td>
                <td class="px-4 py-3">${this.formatQty(shares)}</td>
                <td class="px-4 py-3">${this.formatMoney(investAmount)}</td>
                <td class="px-4 py-3">${allocationPct.toFixed(1)}%</td>
                <td class="px-4 py-3 text-red-400">${this.formatMoney(riskAmount)}</td>
                <td class="px-4 py-3 text-green-400">${probability.toFixed(0)}%</td>
                <td class="px-4 py-3">${fundHTML}</td>
                <td class="px-4 py-3">
                    <button class="detail-btn bg-gray-700 hover:bg-gray-600 px-3 py-1 rounded text-sm" data-symbol="${symbol}">
                        Detail
                    </button>
                </td>
            `;

            // Click to open modal
            row.querySelector('.detail-btn').addEventListener('click', (e) => {
                e.stopPropagation();
                this.showStockModal(signal);
            });

            tbody.appendChild(row);
        });
    }

    showStockModal(signal) {
        this.currentSignal = signal;
        const modal = document.getElementById('stockModal');
        const symbol = signal.stock.symbol || signal.stock.Symbol || 'N/A';
        const name = signal.stock.name || signal.stock.Name || symbol;
        const guide = signal.guide || {};

        document.getElementById('modalTitle').textContent = `${symbol} - ${name}`;
        document.getElementById('modalEntry').textContent = this.formatPrice(guide.entry_price || guide.EntryPrice || 0);
        document.getElementById('modalStopLoss').textContent = `${this.formatPrice(guide.stop_loss || guide.StopLoss || 0)} (${(guide.stop_loss_pct || guide.StopLossPct || 0).toFixed(1)}%)`;
        document.getElementById('modalTarget1').textContent = `${this.formatPrice(guide.target_1 || guide.Target1 || 0)} (+${(guide.target_1_pct || guide.Target1Pct || 0).toFixed(1)}%)`;
        document.getElementById('modalTarget2').textContent = `${this.formatPrice(guide.target_2 || guide.Target2 || 0)} (+${(guide.target_2_pct || guide.Target2Pct || 0).toFixed(1)}%)`;
        document.getElementById('modalShares').value = guide.position_size || guide.PositionSize || 0;
        document.getElementById('modalReason').textContent = signal.reason || 'N/A';

        // Fundamentals section
        const fund = signal.fundamentals;
        const fundSection = document.getElementById('modalFundamentals');
        if (fund) {
            fundSection.classList.remove('hidden');
            const isKR = (symbol.length === 6 && /^\d+$/.test(symbol));
            const mcap = fund.marketCap || fund.MarketCap || 0;
            const mcapStr = isKR
                ? (mcap >= 1e12 ? `₩${(mcap/1e12).toFixed(1)}T` : `₩${(mcap/1e8).toFixed(0)}B`)
                : (mcap >= 1e9 ? `$${(mcap/1e9).toFixed(1)}B` : `$${(mcap/1e6).toFixed(0)}M`);
            const tpe = fund.trailingPE || fund.TrailingPE || 0;
            const fpe = fund.forwardPE || fund.ForwardPE || 0;
            const de = fund.debtToEquity || fund.DebtToEquity || 0;
            const pm = (fund.profitMargins || fund.ProfitMargins || 0) * 100;
            const w52 = (fund.fiftyTwoWeekChg || fund.FiftyTwoWeekChg || 0) * 100;
            const revg = (fund.revenueGrowth || fund.RevenueGrowth || 0) * 100;
            const roe = (fund.returnOnEquity || fund.ReturnOnEquity || 0) * 100;

            document.getElementById('fundMarketCap').textContent = mcapStr;
            document.getElementById('fundTrailingPE').textContent = tpe > 0 ? tpe.toFixed(1) : 'N/A';
            document.getElementById('fundForwardPE').textContent = fpe > 0 ? fpe.toFixed(1) : 'N/A';

            const deEl = document.getElementById('fundDebtEquity');
            deEl.textContent = `${de.toFixed(1)}%`;
            deEl.className = de > 200 ? 'font-semibold text-red-400' : 'font-semibold';

            const pmEl = document.getElementById('fundProfitMargin');
            pmEl.textContent = `${pm.toFixed(1)}%`;
            pmEl.className = pm < -10 ? 'font-semibold text-red-400' : pm > 10 ? 'font-semibold text-green-400' : 'font-semibold';

            const w52El = document.getElementById('fund52WChange');
            w52El.textContent = `${w52 >= 0 ? '+' : ''}${w52.toFixed(1)}%`;
            w52El.className = w52 < -30 ? 'font-semibold text-red-400' : w52 > 0 ? 'font-semibold text-green-400' : 'font-semibold text-yellow-400';

            document.getElementById('fundRevGrowth').textContent = `${revg >= 0 ? '+' : ''}${revg.toFixed(1)}%`;
            document.getElementById('fundROE').textContent = `${roe.toFixed(1)}%`;
        } else {
            fundSection.classList.add('hidden');
        }

        this.updateModalInvestment(guide.position_size || guide.PositionSize || 0);

        // Show scanner-specific buttons
        document.getElementById('excludeBtn').classList.remove('hidden');
        document.getElementById('applySharesBtn').classList.remove('hidden');

        // Render chart
        const candles = signal.candles || [];
        if (candles.length > 0) {
            renderChart('chartContainer', candles, guide);
        }

        modal.classList.remove('hidden');
    }

    hideStockModal() {
        document.getElementById('stockModal').classList.add('hidden');
        this.currentSignal = null;
    }

    updateModalInvestment(shares) {
        if (!this.currentSignal || !this.currentSignal.guide) return;

        const guide = this.currentSignal.guide;
        const entryPrice = guide.entry_price || guide.EntryPrice || 0;
        const stopLoss = guide.stop_loss || guide.StopLoss || 0;
        const riskPerShare = entryPrice - stopLoss;

        const investment = shares * entryPrice;
        const riskAmount = shares * riskPerShare;
        const riskPct = (riskAmount / this.capital) * 100;

        document.getElementById('modalInvestment').textContent = this.formatMoney(investment);
        document.getElementById('modalRiskAmount').textContent = this.formatMoney(riskAmount);
        document.getElementById('modalRiskPct').textContent = `${riskPct.toFixed(2)}%`;
    }

    excludeCurrentStock() {
        if (!this.currentSignal) return;

        const symbol = this.currentSignal.stock.symbol || this.currentSignal.stock.Symbol;
        this.excluded.add(symbol);
        this.hideStockModal();
        this.recalculate();
    }

    applyShares() {
        if (!this.currentSignal || !this.currentSignal.guide) return;

        const shares = parseInt(document.getElementById('modalShares').value) || 0;
        if (shares < 1) {
            alert('Shares must be at least 1');
            return;
        }

        const guide = this.currentSignal.guide;
        const entryPrice = guide.entry_price || guide.EntryPrice || 0;
        const stopLoss = guide.stop_loss || guide.StopLoss || 0;
        const riskPerShare = entryPrice - stopLoss;

        guide.position_size = guide.PositionSize = shares;
        guide.invest_amount = guide.InvestAmount = shares * entryPrice;
        guide.risk_amount = guide.RiskAmount = shares * riskPerShare;
        guide.risk_pct = guide.RiskPct = (guide.risk_amount / this.capital) * 100;
        guide.allocation_pct = guide.AllocationPct = (guide.invest_amount / this.capital) * 100;

        this.hideStockModal();
        this.recalculate();
    }

    async runScan() {
        const capital = parseFloat(document.getElementById('capitalInput').value) || 200;
        const minCapital = this.isKRW() ? 100000 : 100;
        const currLabel = this.isKRW() ? '₩' : '$';
        if (capital < minCapital) {
            alert(`Minimum capital is ${currLabel}${minCapital.toLocaleString()}`);
            return;
        }

        const scanLabel = this.isCrypto() ? 'Crypto Volatility Breakout Scan'
            : this.isKR() ? 'KR Adaptive Multi-Strategy Scan' : 'Adaptive Multi-Strategy Scan';
        this.showLoading(true, scanLabel, 'Starting...');

        try {
            // Fire-and-forget: start scan
            const mq = this.marketQuery('&');
            const startRes = await fetch(`/api/scan?capital=${capital}${mq}`, { method: 'POST' });
            const startData = await startRes.json();

            if (startData.status === 'already_running') {
                // Scan already in progress, just start polling
            } else if (startData.status !== 'started') {
                this.showLoading(false);
                alert('Failed to start scan: ' + JSON.stringify(startData));
                return;
            }

            // Poll for progress
            const statusMq = this.marketQuery();
            this._scanPoll = setInterval(async () => {
                try {
                    const res = await fetch('/api/scan/status' + statusMq);
                    const st = await res.json();

                    const title = document.getElementById('loadingTitle');
                    const detail = document.getElementById('loadingDetail');

                    if (st.status === 'running') {
                        if (title) title.textContent = `Scanned ${st.scanned} | Found ${st.found} signals`;
                        if (detail) detail.textContent = st.message || '';
                    } else if (st.status === 'done') {
                        this.stopScanPoll();
                        if (detail) detail.textContent = 'Loading results...';
                        await this.fetchScanResult(capital);
                    } else if (st.status === 'error') {
                        this.stopScanPoll();
                        this.showLoading(false);
                        alert('Scan failed: ' + (st.error || 'Unknown error'));
                    }
                } catch (err) {
                    // Network blip — keep polling, don't abort
                    console.warn('Poll failed, retrying...', err);
                }
            }, 2000);

        } catch (err) {
            this.showLoading(false);
            alert('Failed to start scan: ' + err.message);
        }
    }

    stopScanPoll() {
        if (this._scanPoll) {
            clearInterval(this._scanPoll);
            this._scanPoll = null;
        }
    }

    async loadLastResult(noAutoSwitch = false) {
        try {
            const mq = this.marketQuery();
            let st = await fetch('/api/scan/status' + mq).then(r => r.json());

            // On initial page load only: if current market has no result, auto-switch
            if (!noAutoSwitch && st.status === 'idle') {
                const otherMarkets = ['us', 'kr', 'crypto'].filter(m => m !== this.market);
                for (const otherMarket of otherMarkets) {
                    const otherMq = otherMarket === 'us' ? '' : `?market=${otherMarket}`;
                    const otherSt = await fetch('/api/scan/status' + otherMq).then(r => r.json());
                    if (otherSt.status === 'done') {
                        this.market = otherMarket;
                        document.querySelectorAll('.market-btn').forEach(btn => {
                            btn.classList.toggle('active', btn.dataset.market === otherMarket);
                            if (btn.dataset.market !== otherMarket) {
                                btn.classList.add('text-gray-400');
                            } else {
                                btn.classList.remove('text-gray-400');
                            }
                        });
                        st = otherSt;
                        break;
                    }
                }
            }

            if (st.status === 'done') {
                const capital = parseFloat(document.getElementById('capitalInput').value) || this.capital;
                await this.fetchScanResult(capital);
            } else if (st.status === 'running') {
                this.showLoading(true, 'Scan in progress', st.message || '');
                const pollMq = this.marketQuery();
                this._scanPoll = setInterval(async () => {
                    try {
                        const poll = await fetch('/api/scan/status' + pollMq).then(r => r.json());
                        const title = document.getElementById('loadingTitle');
                        const detail = document.getElementById('loadingDetail');
                        if (poll.status === 'running') {
                            if (title) title.textContent = `Scanned ${poll.scanned} | Found ${poll.found} signals`;
                            if (detail) detail.textContent = poll.message || '';
                        } else if (poll.status === 'done') {
                            this.stopScanPoll();
                            await this.fetchScanResult(this.capital);
                        } else if (poll.status === 'error') {
                            this.stopScanPoll();
                            this.showLoading(false);
                        }
                    } catch (err) { /* retry */ }
                }, 2000);
            } else {
                const marketLabel = this.isCrypto() ? 'Crypto' : this.isKR() ? 'KR' : 'US';
                document.getElementById('scanMeta').textContent = `No ${marketLabel} scan result — click Scan to start`;
                // Hide results but keep controls (scan button) visible
                document.getElementById('summaryCards').classList.add('hidden');
                document.getElementById('signalsSection').classList.add('hidden');
                document.getElementById('controls').classList.remove('hidden');
            }
        } catch (err) {
            // server not reachable, ignore on auto-load
        }
    }

    async fetchScanResult(capital) {
        try {
            // Fetch result for current market
            const mq = this.marketQuery();
            let res = await fetch('/api/scan/result' + mq);
            if (!res.ok) {
                // No result for this market — clear UI
                this.showLoading(false);
                this.signals = [];
                this.excluded.clear();
                this.recalculate();
                this.hideUI();
                const label = this.isCrypto() ? 'Crypto' : this.isKR() ? 'KR' : 'US';
                document.getElementById('scanMeta').textContent = `No ${label} scan result — click Scan to start`;
                return;
            }
            const data = await res.json();

            this.showLoading(false);

            // Use server capital if available, else input value
            this.capital = data.capital || capital;
            document.getElementById('capitalInput').value = this.capital;

            this.signals = (data.signals || []).map(s => this.normalizeSignal(s));
            this.excluded.clear();
            this.recalculate();
            this.showUI();

            const meta = [];
            if (data.universes_used && data.universes_used.length > 0) {
                meta.push(data.universes_used.join(' → '));
            }
            meta.push(`${data.total_scanned || 0} scanned`);
            if (data.avg_prob > 0) meta.push(`avg ${data.avg_prob.toFixed(0)}% prob`);
            if (data.expansions > 0) meta.push(`${data.expansions}x expanded`);
            if (data.fundamentals_filtered > 0) meta.push(`${data.fundamentals_filtered} rejected by fundamentals`);
            meta.push(data.scan_time || '');
            document.getElementById('scanMeta').textContent = meta.filter(Boolean).join(' | ');
            document.getElementById('recalculateBtn').classList.remove('hidden');

            // Regime display
            this.updateRegimeBar(data);
        } catch (err) {
            this.showLoading(false);
            alert('Failed to load scan result: ' + err.message);
        }
    }

    updateRegimeBar(data) {
        const bar = document.getElementById('regimeBar');
        if (!data.regime) {
            bar.classList.add('hidden');
            return;
        }
        bar.classList.remove('hidden');

        const badge = document.getElementById('regimeBadge');
        const regime = data.regime.toUpperCase();
        badge.textContent = regime;
        badge.className = 'px-3 py-1 rounded-full text-sm font-bold ';
        if (data.regime === 'bull') {
            badge.className += 'bg-green-900 text-green-300 border border-green-700';
        } else if (data.regime === 'bear') {
            badge.className += 'bg-red-900 text-red-300 border border-red-700';
        } else {
            badge.className += 'bg-yellow-900 text-yellow-300 border border-yellow-700';
        }

        // Benchmark info
        const bench = document.getElementById('regimeBenchmark');
        if (data.benchmark_price > 0) {
            const fmt = this.isKR() ? '₩' + Math.round(data.benchmark_price).toLocaleString()
                      : this.isCrypto() ? '₩' + Math.round(data.benchmark_price).toLocaleString()
                      : '$' + data.benchmark_price.toFixed(2);
            const ma20 = this.isKR() || this.isCrypto()
                ? Math.round(data.benchmark_ma20).toLocaleString()
                : data.benchmark_ma20.toFixed(2);
            const ma50 = this.isKR() || this.isCrypto()
                ? Math.round(data.benchmark_ma50).toLocaleString()
                : data.benchmark_ma50.toFixed(2);
            const priceVsMa20 = data.benchmark_price > data.benchmark_ma20 ? '>' : '<';
            const priceVsMa50 = data.benchmark_price > data.benchmark_ma50 ? '>' : '<';
            bench.innerHTML = `Price ${fmt} ${priceVsMa20} MA20 ${ma20} | ${priceVsMa50} MA50 ${ma50} | RSI ${data.benchmark_rsi.toFixed(1)}`;
        } else {
            bench.textContent = '';
        }

        // Active strategies
        const strats = document.getElementById('regimeStrategies');
        strats.textContent = (data.active_strategies || []).join(', ');
    }

    showLoading(show, title, detail) {
        const el = document.getElementById('loading');
        el.classList.toggle('hidden', !show);

        if (show) {
            document.getElementById('loadingTitle').textContent = title || 'Scanning stocks...';
            document.getElementById('loadingDetail').textContent = detail || '';
            const timerEl = document.getElementById('loadingTimer');
            const start = Date.now();
            timerEl.textContent = '0s elapsed';
            this._loadingTimer = setInterval(() => {
                const sec = Math.floor((Date.now() - start) / 1000);
                const min = Math.floor(sec / 60);
                timerEl.textContent = min > 0 ? `${min}m ${sec % 60}s elapsed` : `${sec}s elapsed`;
            }, 1000);
        } else {
            if (this._loadingTimer) {
                clearInterval(this._loadingTimer);
                this._loadingTimer = null;
            }
        }
    }

    // ==================== HISTORY TAB ====================
    async loadTradeHistory() {
        try {
            const mq = this.marketQuery();
            const res = await fetch('/api/trade-history' + mq);
            const data = await res.json();

            this.renderHistorySummary(data.summary || {});
            this.renderStrategyPerformance(data.summary || {});
            this.renderHistoryTable(data.records || []);
        } catch (e) {
            console.error('Failed to load trade history:', e);
        }
    }

    renderHistorySummary(summary) {
        const fmt = (v) => this.formatMoney(v);
        const total = summary.total_trades || 0;
        const buys = summary.buy_count || 0;
        const sells = summary.sell_count || 0;
        const wins = summary.win_count || 0;
        const losses = summary.loss_count || 0;
        const winRate = summary.win_rate || 0;
        const pnl = summary.total_realized_pnl || 0;
        const commission = summary.total_commission || 0;
        // Net = Realized - Commission (반올림 후 계산하여 표시 일관성 보장)
        const netPnl = this.isKRW()
            ? Math.round(pnl) - Math.round(commission)
            : parseFloat((pnl - commission).toFixed(2));

        document.getElementById('histTotalTrades').textContent = total;
        document.getElementById('histBuys').textContent = buys;
        document.getElementById('histSells').textContent = sells;
        document.getElementById('histWinRate').textContent = sells > 0 ? `${winRate.toFixed(1)}%` : '--%';
        document.getElementById('histWins').textContent = wins;
        document.getElementById('histLosses').textContent = losses;

        const pnlEl = document.getElementById('histPnL');
        pnlEl.textContent = (pnl >= 0 ? '+' : '') + fmt(pnl);
        pnlEl.className = `text-2xl font-bold ${pnl > 0 ? 'pnl-positive' : pnl < 0 ? 'pnl-negative' : 'pnl-neutral'}`;

        document.getElementById('histCommission').textContent = fmt(commission);

        const netEl = document.getElementById('histNetPnL');
        netEl.textContent = (netPnl >= 0 ? '+' : '') + fmt(netPnl);
        netEl.className = `text-2xl font-bold ${netPnl > 0 ? 'pnl-positive' : netPnl < 0 ? 'pnl-negative' : 'pnl-neutral'}`;
    }

    renderStrategyPerformance(summary) {
        const container = document.getElementById('strategyPerf');
        const byStrategy = summary.by_strategy || {};

        if (Object.keys(byStrategy).length === 0) {
            container.innerHTML = '';
            return;
        }

        const fmt = (v) => this.formatMoney(v);
        container.innerHTML = Object.entries(byStrategy).map(([name, s]) => {
            const stratClass = `strategy-${name}`;
            const pnlClass = s.pnl > 0 ? 'pnl-positive' : s.pnl < 0 ? 'pnl-negative' : 'pnl-neutral';
            const netClass = s.net_pnl > 0 ? 'pnl-positive' : s.net_pnl < 0 ? 'pnl-negative' : 'pnl-neutral';

            return `
                <div class="bg-gray-800 rounded-xl p-4 border border-gray-700">
                    <div class="flex items-center gap-2 mb-3">
                        <span class="strategy-badge ${stratClass}">${name}</span>
                    </div>
                    <div class="grid grid-cols-2 gap-2 text-sm">
                        <div><span class="text-gray-500">Trades:</span> ${s.trades}</div>
                        <div><span class="text-gray-500">Win Rate:</span> ${s.trades > 0 ? s.win_rate.toFixed(1) : 0}%</div>
                        <div><span class="text-gray-500">Wins:</span> <span class="text-green-400">${s.wins}</span></div>
                        <div><span class="text-gray-500">Losses:</span> <span class="text-red-400">${s.losses}</span></div>
                        <div><span class="text-gray-500">P&L:</span> <span class="${pnlClass}">${fmt(s.pnl)}</span></div>
                        <div><span class="text-gray-500">Net:</span> <span class="${netClass}">${fmt(s.net_pnl)}</span></div>
                    </div>
                </div>
            `;
        }).join('');
    }

    renderHistoryTable(records) {
        const tbody = document.getElementById('historyTable');
        const noRecords = document.getElementById('histNoRecords');
        const countEl = document.getElementById('histRecordCount');

        if (records.length === 0) {
            tbody.innerHTML = '';
            noRecords.classList.remove('hidden');
            countEl.textContent = '';
            return;
        }

        noRecords.classList.add('hidden');
        countEl.textContent = `${records.length} records`;

        // Reverse to show newest first
        const sorted = [...records].reverse();

        tbody.innerHTML = sorted.map(r => {
            const date = r.timestamp ? new Date(r.timestamp).toLocaleDateString('ko-KR', {month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit'}) : '--';
            const isSell = r.side === 'sell';
            const sideClass = isSell ? 'text-red-400' : 'text-green-400';
            const sideLabel = isSell ? 'SELL' : 'BUY';

            let pnlHtml = '';
            if (isSell && r.pnl !== undefined) {
                const pnlClass = r.pnl > 0 ? 'pnl-positive' : r.pnl < 0 ? 'pnl-negative' : 'pnl-neutral';
                const sign = r.pnl >= 0 ? '+' : '';
                pnlHtml = `<span class="${pnlClass}">${sign}${this.formatMoney(r.pnl)}</span>`;
                if (r.pnl_pct) {
                    pnlHtml += ` <span class="${pnlClass} text-xs">(${sign}${r.pnl_pct.toFixed(1)}%)</span>`;
                }
            }

            const stratClass = r.strategy ? `strategy-${r.strategy}` : '';
            const stratLabel = r.strategy || '';

            const reasonBadge = r.reason ? this.getReasonBadge(r.reason) : '';

            return `
                <tr class="text-sm">
                    <td class="px-4 py-3 text-gray-400 whitespace-nowrap">${date}</td>
                    <td class="px-4 py-3 font-semibold text-blue-400">${r.symbol}${r.name ? `<span class="text-gray-400 font-normal text-xs ml-1">${r.name}</span>` : ''}</td>
                    <td class="px-4 py-3 ${sideClass} font-medium">${sideLabel}</td>
                    <td class="px-4 py-3">${stratLabel ? `<span class="strategy-badge ${stratClass} text-xs">${stratLabel}</span>` : ''}</td>
                    <td class="px-4 py-3">${r.quantity}</td>
                    <td class="px-4 py-3">${this.formatPrice(r.price || 0)}</td>
                    <td class="px-4 py-3">${this.formatMoney(r.amount || 0)}</td>
                    <td class="px-4 py-3">${pnlHtml}</td>
                    <td class="px-4 py-3">${reasonBadge}</td>
                </tr>
            `;
        }).join('');
    }

    getReasonBadge(reason) {
        const reasonColors = {
            'signal': 'bg-blue-600',
            'stop_loss': 'bg-red-600',
            'target1': 'bg-green-600',
            'target2': 'bg-green-700',
            'invalidation': 'bg-yellow-600',
        };
        // Handle time_stop variants like "time_stop_7d (P&L: -0.5%)"
        let displayReason = reason;
        let colorClass = 'bg-gray-600';
        if (reason.startsWith('time_stop')) {
            displayReason = 'time_stop';
            colorClass = 'bg-orange-600';
        } else {
            colorClass = reasonColors[reason] || 'bg-gray-600';
        }
        return `<span class="${colorClass} px-2 py-0.5 rounded text-xs">${displayReason}</span>`;
    }

    formatUSD(amount) {
        if (amount >= 1000000) {
            return `$${(amount / 1000000).toFixed(2)}M`;
        } else if (amount >= 1000) {
            return `$${(amount / 1000).toFixed(1)}K`;
        }
        return `$${amount.toFixed(2)}`;
    }

    formatKRW(amount) {
        const abs = Math.abs(amount);
        const sign = amount < 0 ? '-' : '';
        if (abs >= 100000000) {
            return `${sign}₩${(abs / 100000000).toFixed(1)}억`;
        } else if (abs >= 10000) {
            return `${sign}₩${(abs / 10000).toFixed(0)}만`;
        }
        return `${sign}₩${Math.round(abs).toLocaleString()}`;
    }

    formatMoney(amount) {
        return this.isKRW() ? this.formatKRW(amount) : this.formatUSD(amount);
    }

    formatPrice(price) {
        if (this.isKRW()) {
            return `₩${Math.round(price).toLocaleString()}`;
        }
        return `$${price.toFixed(2)}`;
    }

    formatQty(qty) {
        if (this.isCrypto()) {
            if (qty >= 100) return qty.toFixed(2);
            if (qty >= 1) return qty.toFixed(4);
            if (qty >= 0.0001) return qty.toFixed(6);
            return qty.toFixed(8);
        }
        return Math.floor(qty).toString();
    }
}

// Initialize app when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    window.app = new TravelerApp();
});
