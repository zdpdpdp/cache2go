/*
 * Simple caching library with expiration capabilities
 *     Copyright (c) 2012, Radu Ioan Fericean
 *                   2013-2017, Christian Muehlhaeuser <muesli@gmail.com>
 *
 *   For license see LICENSE.txt
 */

package cache2go

import (
	"sync"
)

var (
	//全局变量
	cache = make(map[string]*CacheTable)
	//全局锁,因为 cache 是 map,并发不安全,加个锁
	mutex sync.RWMutex
)

//创建或者返回一个已存在的 table
func Cache(table string) *CacheTable {

	//如果被加了 写锁,那么读锁是加不了的
	mutex.RLock()
	t, ok := cache[table]
	mutex.RUnlock()

	//为什么要双重检查??
	//其实可以无论读写都直接 lock 的
	//但是消耗太高了
	//参考单例模式双重锁机制 https://blog.csdn.net/gangjindianzi/article/details/78689713

	if !ok {
		mutex.Lock()
		t, ok = cache[table]
		// 双重检查机制
		if !ok {
			t = &CacheTable{
				name:  table,
				items: make(map[interface{}]*CacheItem),
			}
			cache[table] = t
		}
		mutex.Unlock()
	}

	return t
}
