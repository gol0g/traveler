// Traveler Dashboard - Chart Rendering with Lightweight Charts

let chartInstance = null;
let candleSeries = null;
let ma20Series = null;
let ma50Series = null;

/**
 * Render a candlestick chart with moving averages and price levels
 * @param {string} containerId - DOM element ID for the chart
 * @param {Array} candles - Array of candle objects
 * @param {Object} guide - Trade guide with entry, stop, targets
 */
function renderChart(containerId, candles, guide) {
    const container = document.getElementById(containerId);
    if (!container) return;

    // Clear existing chart
    container.innerHTML = '';

    // Create chart
    chartInstance = LightweightCharts.createChart(container, {
        width: container.clientWidth,
        height: container.clientHeight,
        layout: {
            background: { type: 'solid', color: '#111827' },
            textColor: '#9ca3af',
        },
        grid: {
            vertLines: { color: '#1f2937' },
            horzLines: { color: '#1f2937' },
        },
        crosshair: {
            mode: LightweightCharts.CrosshairMode.Normal,
        },
        rightPriceScale: {
            borderColor: '#374151',
        },
        timeScale: {
            borderColor: '#374151',
            timeVisible: true,
        },
    });

    // Convert candles to chart format
    const chartData = candles.map(c => {
        const time = c.time || c.Time;
        let timestamp;

        if (typeof time === 'string') {
            // Parse ISO date string
            timestamp = new Date(time).getTime() / 1000;
        } else if (typeof time === 'number') {
            timestamp = time;
        } else {
            timestamp = Date.now() / 1000;
        }

        return {
            time: timestamp,
            open: c.open || c.Open,
            high: c.high || c.High,
            low: c.low || c.Low,
            close: c.close || c.Close,
        };
    }).filter(c => !isNaN(c.time) && c.time > 0);

    // Sort by time
    chartData.sort((a, b) => a.time - b.time);

    if (chartData.length === 0) {
        container.innerHTML = '<div class="flex items-center justify-center h-full text-gray-500">No chart data available</div>';
        return;
    }

    // Add candlestick series
    candleSeries = chartInstance.addCandlestickSeries({
        upColor: '#22c55e',
        downColor: '#ef4444',
        borderDownColor: '#ef4444',
        borderUpColor: '#22c55e',
        wickDownColor: '#ef4444',
        wickUpColor: '#22c55e',
    });
    candleSeries.setData(chartData);

    // Calculate and add MA20
    const ma20Data = calculateMA(chartData, 20);
    if (ma20Data.length > 0) {
        ma20Series = chartInstance.addLineSeries({
            color: '#f59e0b',
            lineWidth: 1,
            title: 'MA20',
        });
        ma20Series.setData(ma20Data);
    }

    // Calculate and add MA50
    const ma50Data = calculateMA(chartData, 50);
    if (ma50Data.length > 0) {
        ma50Series = chartInstance.addLineSeries({
            color: '#8b5cf6',
            lineWidth: 1,
            title: 'MA50',
        });
        ma50Series.setData(ma50Data);
    }

    // Add price levels from guide
    if (guide) {
        const entryPrice = guide.entry_price || guide.EntryPrice;
        const stopLoss = guide.stop_loss || guide.StopLoss;
        const target1 = guide.target_1 || guide.Target1;
        const target2 = guide.target_2 || guide.Target2;

        // Entry price line (blue)
        if (entryPrice) {
            candleSeries.createPriceLine({
                price: entryPrice,
                color: '#3b82f6',
                lineWidth: 2,
                lineStyle: LightweightCharts.LineStyle.Solid,
                axisLabelVisible: true,
                title: 'Entry',
            });
        }

        // Stop loss line (red dashed)
        if (stopLoss) {
            candleSeries.createPriceLine({
                price: stopLoss,
                color: '#ef4444',
                lineWidth: 1,
                lineStyle: LightweightCharts.LineStyle.Dashed,
                axisLabelVisible: true,
                title: 'Stop',
            });
        }

        // Target 1 line (green dashed)
        if (target1) {
            candleSeries.createPriceLine({
                price: target1,
                color: '#22c55e',
                lineWidth: 1,
                lineStyle: LightweightCharts.LineStyle.Dashed,
                axisLabelVisible: true,
                title: 'T1',
            });
        }

        // Target 2 line (green dotted)
        if (target2) {
            candleSeries.createPriceLine({
                price: target2,
                color: '#22c55e',
                lineWidth: 1,
                lineStyle: LightweightCharts.LineStyle.Dotted,
                axisLabelVisible: true,
                title: 'T2',
            });
        }
    }

    // Fit content
    chartInstance.timeScale().fitContent();

    // Handle resize
    const resizeObserver = new ResizeObserver(entries => {
        if (entries.length === 0 || entries[0].target !== container) return;
        const { width, height } = entries[0].contentRect;
        chartInstance.applyOptions({ width, height });
    });
    resizeObserver.observe(container);
}

/**
 * Calculate Simple Moving Average
 * @param {Array} data - Array of candle objects with {time, close}
 * @param {number} period - MA period
 * @returns {Array} - Array of {time, value}
 */
function calculateMA(data, period) {
    const result = [];

    for (let i = period - 1; i < data.length; i++) {
        let sum = 0;
        for (let j = 0; j < period; j++) {
            sum += data[i - j].close;
        }
        result.push({
            time: data[i].time,
            value: sum / period,
        });
    }

    return result;
}

/**
 * Destroy current chart instance
 */
function destroyChart() {
    if (chartInstance) {
        chartInstance.remove();
        chartInstance = null;
        candleSeries = null;
        ma20Series = null;
        ma50Series = null;
    }
}

// Export for use in other modules if needed
if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderChart, destroyChart, calculateMA };
}
