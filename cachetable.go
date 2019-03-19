/*
 * Simple caching library with expiration capabilities
 *     Copyright (c) 2013-2017, Christian Muehlhaeuser <muesli@gmail.com>
 *
 *   For license see LICENSE.txt
 */

package cache2go

import (
	"log"
	"sort"
	"sync"
	"time"
)

// CacheTable is a table within the cache
//核心类
type CacheTable struct {
	sync.RWMutex //读写锁

	// The table's name.
	name string //表名
	// All cached items.
	items map[interface{}]*CacheItem //存储

	// Timer responsible for triggering cleanup.
	cleanupTimer *time.Timer //定时清理计时器
	// Current timer duration.
	cleanupInterval time.Duration //多久清理一次

	// The logger used for this table.
	logger *log.Logger

	// Callback method triggered when trying to load a non-existing key.
	//查一个不存在 key 时的回调
	loadData func(key interface{}, args ...interface{}) *CacheItem
	// Callback method triggered when adding a new item to the cache.
	//添加元素时回调
	addedItem func(item *CacheItem)
	// Callback method triggered before deleting an item from the cache.
	//删除 key 时的回调
	aboutToDeleteItem func(item *CacheItem)
}

// Count returns how many items are currently stored in the cache.
//计数
func (table *CacheTable) Count() int {
	table.RLock()
	defer table.RUnlock()
	return len(table.items)
}

// Foreach all items
//遍历传入迭代器
func (table *CacheTable) Foreach(trans func(key interface{}, item *CacheItem)) {
	table.RLock()
	defer table.RUnlock()

	for k, v := range table.items {
		trans(k, v)
	}
}

// SetDataLoader configures a data-loader callback, which will be called when
// trying to access a non-existing key. The key and 0...n additional arguments
// are passed to the callback function.
//设置方法
func (table *CacheTable) SetDataLoader(f func(interface{}, ...interface{}) *CacheItem) {
	table.Lock()
	defer table.Unlock()
	table.loadData = f
}

// SetAddedItemCallback configures a callback, which will be called every time
// a new item is added to the cache.
func (table *CacheTable) SetAddedItemCallback(f func(*CacheItem)) {
	table.Lock()
	defer table.Unlock()
	table.addedItem = f
}

// SetAboutToDeleteItemCallback configures a callback, which will be called
// every time an item is about to be removed from the cache.
func (table *CacheTable) SetAboutToDeleteItemCallback(f func(*CacheItem)) {
	table.Lock()
	defer table.Unlock()
	table.aboutToDeleteItem = f
}

// SetLogger sets the logger to be used by this cache table.
func (table *CacheTable) SetLogger(logger *log.Logger) {
	table.Lock()
	defer table.Unlock()
	table.logger = logger
}

// Expiration check loop, triggered by a self-adjusting timer.
//过期检查
func (table *CacheTable) expirationCheck() {
	table.Lock()
	if table.cleanupTimer != nil {
		table.cleanupTimer.Stop() //停止当前的触发器时间
	}
	if table.cleanupInterval > 0 {
		table.log("Expiration check triggered after", table.cleanupInterval, "for table", table.name)
	} else {
		table.log("Expiration check installed for table", table.name)
	}

	// To be more accurate with timers, we would need to update 'now' on every
	// loop iteration. Not sure it's really efficient though.
	now := time.Now()
	smallestDuration := 0 * time.Second
	for key, item := range table.items {
		// Cache values so we don't keep blocking the mutex.
		item.RLock()
		lifeSpan := item.lifeSpan     //过期时长
		accessedOn := item.accessedOn //上一次被访问的时间
		item.RUnlock()

		if lifeSpan == 0 { //无过期时长,continue
			continue
		}

		//如果上一次被访问的时间到现在大于过期时间 那当然要清除啦
		if now.Sub(accessedOn) >= lifeSpan {
			// Item has excessed its lifespan.
			table.deleteInternal(key)
		} else {
			//找到最小的过期时长,下一次清除垃圾就使用这个时间
			// Find the item chronologically closest to its end-of-lifespan.
			if smallestDuration == 0 || lifeSpan-now.Sub(accessedOn) < smallestDuration {
				smallestDuration = lifeSpan - now.Sub(accessedOn)
			}
		}
	}

	// Setup the interval for the next cleanup run.
	table.cleanupInterval = smallestDuration
	if smallestDuration > 0 {
		//多久后再清理一次
		table.cleanupTimer = time.AfterFunc(smallestDuration, func() {
			//在添加更快过期的元素时,这个定时器会被覆盖掉 真牛逼
			go table.expirationCheck()
		})
	}
	table.Unlock()
}

func (table *CacheTable) addInternal(item *CacheItem) {
	// Careful: do not run this method unless the table-mutex is locked!
	// It will unlock it for the caller before running the callbacks and checks
	table.log("Adding item with key", item.key, "and lifespan of", item.lifeSpan, "to table", table.name)
	table.items[item.key] = item

	// Cache values so we don't keep blocking the mutex.
	expDur := table.cleanupInterval
	addedItem := table.addedItem
	table.Unlock()

	// Trigger callback after adding an item to cache.
	// 添加元素时的回调
	if addedItem != nil {
		addedItem(item)
	}

	//在添加有过期元素的情况下,如果table 的定时清理时间为0(一般为首次) 或者 该元素的过期时间小于定时清理的时间大小,那么要清理一次

	// If we haven't set up any expiration check timer or found a more imminent item.
	if item.lifeSpan > 0 && (expDur == 0 || item.lifeSpan < expDur) {
		table.expirationCheck()
	}
}

// Add adds a key/value pair to the cache.
// Parameter key is the item's cache-key.
// Parameter lifeSpan determines after which time period without an access the item
// will get removed from the cache.
// Parameter data is the item's value.
//添加元素,也可以是覆盖
func (table *CacheTable) Add(key interface{}, lifeSpan time.Duration, data interface{}) *CacheItem {
	item := NewCacheItem(key, lifeSpan, data)

	// Add item to cache.
	table.Lock()
	table.addInternal(item)

	return item
}

func (table *CacheTable) deleteInternal(key interface{}) (*CacheItem, error) {
	r, ok := table.items[key]
	if !ok {
		return nil, ErrKeyNotFound
	}

	// Cache value so we don't keep blocking the mutex.
	aboutToDeleteItem := table.aboutToDeleteItem
	table.Unlock()

	// Trigger callbacks before deleting an item from cache.
	if aboutToDeleteItem != nil {
		aboutToDeleteItem(r)
	}

	r.RLock()
	defer r.RUnlock()
	if r.aboutToExpire != nil {
		//key 被删除时的回调
		r.aboutToExpire(key)
	}

	table.Lock()
	table.log("Deleting item with key", key, "created on", r.createdOn, "and hit", r.accessCount, "times from table", table.name)
	delete(table.items, key)

	return r, nil
}

//删除元素
// Delete an item from the cache.
func (table *CacheTable) Delete(key interface{}) (*CacheItem, error) {
	table.Lock()
	defer table.Unlock()

	return table.deleteInternal(key)
}

//返回是否存在
// Exists returns whether an item exists in the cache. Unlike the Value method
// Exists neither tries to fetch data via the loadData callback nor does it
// keep the item alive in the cache.
func (table *CacheTable) Exists(key interface{}) bool {
	table.RLock()
	defer table.RUnlock()
	_, ok := table.items[key]

	return ok
}

//如果找不到就添加,返回是否添加成功
// NotFoundAdd tests whether an item not found in the cache. Unlike the Exists
// method this also adds data if they key could not be found.
func (table *CacheTable) NotFoundAdd(key interface{}, lifeSpan time.Duration, data interface{}) bool {
	table.Lock()

	if _, ok := table.items[key]; ok {
		table.Unlock()
		return false
	}

	item := NewCacheItem(key, lifeSpan, data)
	table.addInternal(item)

	return true
}

//访问某个 key, 顺便可以做 cache remember
// Value returns an item from the cache and marks it to be kept alive. You can
// pass additional arguments to your DataLoader callback function.
func (table *CacheTable) Value(key interface{}, args ...interface{}) (*CacheItem, error) {
	table.RLock()
	r, ok := table.items[key]
	loadData := table.loadData
	table.RUnlock()

	if ok {
		// Update access counter and timestamp.
		r.KeepAlive()
		return r, nil
	}

	//数据不在缓存中,在 loadData中是否能查找成功? 如果查找成功的话,加到缓存里?
	// Item doesn't exist in cache. Try and fetch it with a data-loader.
	if loadData != nil {
		item := loadData(key, args...)
		if item != nil {
			table.Add(key, item.lifeSpan, item.data)
			return item, nil
		}

		return nil, ErrKeyNotFoundOrLoadable
	}

	return nil, ErrKeyNotFound
}

//清空元素
// Flush deletes all items from this cache table.
func (table *CacheTable) Flush() {
	table.Lock()
	defer table.Unlock()

	table.log("Flushing table", table.name)

	table.items = make(map[interface{}]*CacheItem)
	table.cleanupInterval = 0
	if table.cleanupTimer != nil {
		table.cleanupTimer.Stop()
	}
}

// CacheItemPair maps key to access counter
type CacheItemPair struct {
	Key         interface{}
	AccessCount int64
}

// CacheItemPairList is a slice of CacheIemPairs that implements sort.
// Interface to sort by AccessCount.
type CacheItemPairList []CacheItemPair

func (p CacheItemPairList) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p CacheItemPairList) Len() int           { return len(p) }
func (p CacheItemPairList) Less(i, j int) bool { return p[i].AccessCount > p[j].AccessCount }

//排序啊,被读最多的前 n 个元素
// MostAccessed returns the most accessed items in this cache table
func (table *CacheTable) MostAccessed(count int64) []*CacheItem {
	table.RLock()
	defer table.RUnlock()

	p := make(CacheItemPairList, len(table.items))
	i := 0
	for k, v := range table.items {
		p[i] = CacheItemPair{k, v.accessCount}
		i++
	}
	sort.Sort(p)

	var r []*CacheItem
	c := int64(0)
	for _, v := range p {
		if c >= count {
			break
		}

		item, ok := table.items[v.Key]
		if ok {
			r = append(r, item)
		}
		c++
	}

	return r
}

// Internal logging method for convenience.
func (table *CacheTable) log(v ...interface{}) {
	if table.logger == nil {
		return
	}

	table.logger.Println(v...)
}
