// Package sim 提供确定性的离散事件仿真环境:虚拟时钟、事件堆、
// 链路模型(单向/双向中断、时延抖动、丢包)以及双链路 BFD 拓扑。
//
// 确定性保证:所有随机数来自以场景种子初始化的 *rand.Rand,不存在任何
// 真实时钟读取、goroutine 调度或 map 迭代顺序依赖;同一种子重放产生
// 逐事件一致的时间线。
package sim

import (
	"container/heap"
	"math/rand"
	"time"
)

// EventKind 区分仿真事件的类型。
type EventKind int

const (
	EvTimer   EventKind = iota // 会话定时器到期
	EvControl                  // 控制报文到达
	EvEcho                     // 回声报文到达
	EvAction                   // 场景动作触发(断链等)
	EvSample                   // 状态采样
)

// Event 是仿真事件堆中的一个条目。
type Event struct {
	Time   int64 // 触发时刻(虚拟纳秒)
	Seq    int64 // 同刻事件的稳定排序序号
	Kind   EventKind
	Target uint32 // 目标会话鉴别符(报文/定时器事件)
	Data   any
}

type eventHeap []*Event

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	if h[i].Time != h[j].Time {
		return h[i].Time < h[j].Time
	}
	// 同一虚拟时刻:报文到达优先于定时器到期,使超时判定总是基于最新收包,
	// 避免同刻定时器在报文刷新死线之前误判。
	ri, rj := recvPriority(h[i].Kind), recvPriority(h[j].Kind)
	if ri != rj {
		return ri < rj
	}
	return h[i].Seq < h[j].Seq
}

// recvPriority 规定同刻事件优先级:报文接收(0)先于定时器与动作(1)。
func recvPriority(k EventKind) int {
	if k == EvControl || k == EvEcho {
		return 0
	}
	return 1
}

func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)   { *h = append(*h, x.(*Event)) }
func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return e
}

// Handler 由承载会话的拓扑实现,负责把到期事件投递给对应会话。
type Handler interface {
	HandleTimer(target uint32, data any, now int64)
	HandleControl(target uint32, data any, now int64)
	HandleEcho(target uint32, data any, now int64)
}

// Engine 是单线程离散事件驱动器。
type Engine struct {
	now     int64
	h       eventHeap
	seq     int64
	rng     *rand.Rand
	handler Handler
	stopAt  int64
}

// NewEngine 以种子 seed 构造引擎。种子决定全部抖动/丢包序列。
func NewEngine(seed int64, handler Handler) *Engine {
	e := &Engine{
		rng:     rand.New(rand.NewSource(seed)),
		handler: handler,
		stopAt:  -1,
	}
	heap.Init(&e.h)
	return e
}

// RNG 暴露确定性随机源(链路模型使用)。
func (e *Engine) RNG() *rand.Rand { return e.rng }

// Now 返回当前虚拟时间(纳秒)。
func (e *Engine) Now() int64 { return e.now }

// Schedule 在 at 时刻插入事件。
func (e *Engine) Schedule(at int64, kind EventKind, target uint32, data any) {
	e.seq++
	heap.Push(&e.h, &Event{Time: at, Seq: e.seq, Kind: kind, Target: target, Data: data})
}

// StopAt 设置仿真终止时刻。
func (e *Engine) StopAt(t int64) { e.stopAt = t }

// Run 推进事件循环直到无事件或到达终止时刻,返回最终虚拟时间。
func (e *Engine) Run() int64 {
	for e.h.Len() > 0 {
		ev := heap.Pop(&e.h).(*Event)
		if e.stopAt >= 0 && ev.Time > e.stopAt {
			e.now = e.stopAt
			break
		}
		e.now = ev.Time
		switch ev.Kind {
		case EvTimer:
			e.handler.HandleTimer(ev.Target, ev.Data, e.now)
		case EvControl:
			e.handler.HandleControl(ev.Target, ev.Data, e.now)
		case EvEcho:
			e.handler.HandleEcho(ev.Target, ev.Data, e.now)
		case EvAction:
			fn := ev.Data.(func(now int64))
			fn(e.now)
		case EvSample:
			fn := ev.Data.(func(now int64))
			fn(e.now)
		}
	}
	if e.stopAt >= 0 && e.now < e.stopAt {
		e.now = e.stopAt
	}
	return e.now
}

// ms 把毫秒换算为纳秒。
func ms(n int64) int64 { return n * int64(time.Millisecond) }
