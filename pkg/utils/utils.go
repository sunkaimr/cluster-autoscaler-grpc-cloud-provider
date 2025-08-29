package utils

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func RandStr(length int) string {
	var result []byte
	bytes := []byte("0123456789abcdef")
	rand.Seed(time.Now().UnixNano() + int64(rand.Intn(100)))
	for i := 0; i < length; i++ {
		result = append(result, bytes[rand.Intn(len(bytes))])
	}
	return string(result)
}

func ParseAddr(addr, defHost string, defPort int) (string, int, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return defHost, defPort, errors.New("empty addr")
	}

	host, port := "", ""

	sli := strings.Split(addr, ":")
	switch len(sli) {
	case 0:
		return defHost, defPort, errors.New("empty addr")
	case 1:
		host, port = sli[0], strconv.Itoa(defPort)
	default:
		host, port = sli[0], sli[1]
	}

	if host == "" {
		host = defHost
	}

	if port == "" {
		port = strconv.Itoa(defPort)
	}

	if p, err := strconv.Atoi(port); err != nil {
		return host, defPort, fmt.Errorf("error to parse addr port %s", err)
	} else {
		return host, p, nil
	}
}

// RemoveSubSlices 删除子切片元素
func RemoveSubSlices[T comparable](sli []T, del []T) []T {
	var r []T
	for _, s := range sli {
		needRemove := false
		for _, d := range del {
			if s == d {
				needRemove = true
				break
			}
		}
		if !needRemove {
			r = append(r, s)
		}
	}
	return r
}

// ElementExist 切片是否包含某个元素
func ElementExist(value interface{}, sli interface{}) bool {
	valType := reflect.TypeOf(value)
	sliType := reflect.TypeOf(sli)

	if sliType.Kind() != reflect.Slice {
		return false
	}

	if sliType.Elem() != valType {
		return false
	}

	sliValue := reflect.ValueOf(sli)
	for i := 0; i < sliValue.Len(); i++ {
		if reflect.DeepEqual(sliValue.Index(i).Interface(), value) {
			return true
		}
	}

	return false
}

// IsSliceEqual 2个切片是否相等（忽略元素顺序）
func IsSliceEqual(a, b interface{}) bool {
	// 检查两个参数是否都是切片类型
	valA := reflect.ValueOf(a)
	valB := reflect.ValueOf(b)
	if valA.Kind() != reflect.Slice || valB.Kind() != reflect.Slice {
		return false
	}

	// 获取两个切片的长度和元素类型
	lenA := valA.Len()
	lenB := valB.Len()

	// 如果两个切片长度不等，则直接返回 false
	if lenA != lenB {
		return false
	}
	elemType := valA.Type().Elem()

	// 使用 map 存储 a 中的元素及其出现的次数
	counts := make(map[interface{}]int)
	for i := 0; i < lenA; i++ {
		elem := valA.Index(i).Interface()
		if reflect.TypeOf(elem) != elemType {
			return false
		}
		counts[elem]++
	}

	// 遍历 b 中的元素，如果元素不在 map 中或者次数为 0，则说明两个切片不相等
	for i := 0; i < lenB; i++ {
		elem := valB.Index(i).Interface()
		if reflect.TypeOf(elem) != elemType {
			return false
		}
		if counts[elem] == 0 {
			return false
		}
		counts[elem]--
	}

	// 如果所有元素都匹配，则说明两个切片相等
	return true
}

// IsSubSlices sub是否是sli子切片
func IsSubSlices[T comparable](sub []T, sli []T) bool {
	set := make(map[T]struct{}, len(sli))
	for _, val := range sli {
		set[val] = struct{}{}
	}

	for _, val := range sub {
		if _, ok := set[val]; !ok {
			return false
		}
	}

	return true
}

// Intersect 2个切片的交集
func Intersect[T comparable](o, n []T) []T {
	m := make(map[T]int)
	var arr []T
	for _, v := range o {
		m[v]++
	}
	for _, v := range n {
		m[v]++
		if m[v] > 1 {
			arr = append(arr, v)
		}
	}
	return arr
}

// Subtract 2个切片的差集(从more中去除less)
func Subtract[T comparable](more, less []T) []T {
	m := make(map[T]int)
	var arr []T
	for _, v := range less {
		m[v]++
	}
	for _, v := range more {
		m[v]++
		if m[v] == 1 {
			arr = append(arr, v)
		}
	}
	return arr
}

// Union 2个切片的并集
func Union[T comparable](a, b []T) []T {
	seen := make(map[T]struct{})
	var result []T

	// Add elements from the first slice
	for _, item := range a {
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}

	// Add elements from the second slice
	for _, item := range b {
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}

	return result
}

// RemoveDupElement 切片元素去重
func RemoveDupElement[T comparable](sli []T) []T {
	result := make([]T, 0, len(sli))
	temp := map[T]struct{}{}
	for _, item := range sli {
		if _, ok := temp[item]; !ok {
			temp[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

// IsTimeInRange 时间是否在某个区间内
func IsTimeInRange(t time.Time, startTime, endTime string) (bool, error) {
	var s, e time.Time
	var err error

	if startTime == "" && endTime == "" {
		return false, nil
	}

	if startTime == "" && endTime != "" {
		e, err = time.ParseInLocation("2006-01-02 15:04:05", endTime, t.Location())
		if err != nil {
			return false, err
		}
		return t.Before(e), nil
	}

	if startTime != "" && endTime == "" {
		s, err = time.ParseInLocation("2006-01-02 15:04:05", startTime, t.Location())
		if err != nil {
			return false, err
		}
		return t.After(s), nil
	}

	s, err = time.ParseInLocation("2006-01-02 15:04:05", startTime, t.Location())
	if err != nil {
		return false, err
	}
	e, err = time.ParseInLocation("2006-01-02 15:04:05", endTime, t.Location())
	if err != nil {
		return false, err
	}
	return t.After(s) && t.Before(e), nil
}

// Ternary 模拟三目运算
func Ternary[T any](b bool, v1, v2 any) T {
	if b {
		return v1.(T)
	}
	return v2.(T)
}

func DaysInMonth(year, month int) int {
	date := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	return date.AddDate(0, 1, 0).Add(-time.Hour * 24).Day()
}

// TrimmingStringList 修剪字符串去除多余的空格字符(会去除重复项！！！)
func TrimmingStringList(org, split string) string {
	orgList := strings.Split(org, split)
	trimList := make([]string, 0, len(orgList))
	for _, s := range orgList {
		s1 := strings.TrimSpace(s)
		if len(s1) > 0 {
			trimList = append(trimList, s1)
		}
	}
	trimList = RemoveDupElement(trimList)
	return strings.Join(trimList, split)
}

// TrimmingSQLConditionEnding 修剪where条件后多余的空格和";"
func TrimmingSQLConditionEnding(condition string) string {
	newCondition := condition
	for {
		newCondition = strings.TrimSuffix(strings.TrimSpace(condition), ";")
		if newCondition == condition {
			return newCondition
		}
		condition = newCondition
	}
}

func HttpDo(method, url string, headers, query map[string]string, b string) (*http.Response, []byte, error) {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		//Timeout: time.Second * 5,
	}

	if len(query) > 0 {
		url += "?"
		for k, v := range query {
			url += fmt.Sprintf("%s=%s&", k, v)
		}
	}

	req, err := http.NewRequest(method, url, strings.NewReader(b))
	if err != nil {
		return nil, nil, fmt.Errorf("new http client failed, err:%s", err)
	}

	req.Header.Set("Host", req.URL.Host)
	req.Close = true

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("do http request failed, %s", err)
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("read http response failed, err:%s", err)
	}

	return resp, body, nil
}

// HumanFormatTimeSeconds 将秒转换为便于人阅读的格式
func HumanFormatTimeSeconds(seconds int) string {
	if seconds <= 0 {
		return ""
	}

	days := seconds / (24 * 3600)
	remainingSeconds := seconds % (24 * 3600)
	hours := remainingSeconds / 3600
	remainingSeconds %= 3600
	minutes := remainingSeconds / 60
	seconds = remainingSeconds % 60

	if days > 0 {
		return fmt.Sprintf("%d天 %02d:%02d:%02d", days, hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func IsMail(mail string) bool {
	b, _ := regexp.MatchString(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`, mail)
	return b
}

func SliceToString[T any](sli []T, separator string) string {
	sliStr := make([]string, len(sli))
	for i, v := range sli {
		sliStr[i] = fmt.Sprintf("%v", v)
	}
	return strings.Join(sliStr, separator)
}

func CountSubString(s string, separator string) int {
	count := 0
	ss := strings.Split(s, separator)
	for _, v := range ss {
		if v != "" {
			count++
		}
	}
	return count
}

// hasFractionalPart 检查浮点数的小数部分是否大于 0
func hasFractionalPart(num float64) bool {
	// 使用 math.Trunc 去除小数部分（向零取整）
	truncated := math.Trunc(num)
	// 检查原始数与取整后的数是否相等
	return num != truncated
}

// ExtractStructFieldToSlice 将结构体切片中的某个字段提取出来单独组成一个切片
func ExtractStructFieldToSlice(structSlice any, field string) any {
	sVal := reflect.ValueOf(structSlice)
	if sVal.Kind() != reflect.Slice {
		panic("not a slice")
	}

	sType := sVal.Type().Elem()
	if sType.Kind() != reflect.Struct {
		panic("slice elements must be struct")
	}

	fieldValue, found := sType.FieldByName(field)
	if !found {
		return nil
	}

	newSlice := reflect.MakeSlice(reflect.SliceOf(fieldValue.Type), 0, sVal.Len())

	for i := 0; i < sVal.Len(); i++ {
		newSlice = reflect.Append(newSlice, sVal.Index(i).FieldByName(field))
	}

	return newSlice.Interface()
}

// ConvertStringToIntSlice 将字符串转换为整型切片
func ConvertStringToIntSlice[T int | uint](input, sep string) []T {
	sli := strings.Split(input, sep)
	nums := make([]T, 0, len(sli))

	for _, s := range sli {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}

		if num, err := strconv.Atoi(s); err == nil {
			nums = append(nums, T(num))
		}
	}

	return nums
}

// IsMapSuperset 判断subMap是否是map1的子集
func IsMapSuperset(map1, subMap map[string]string) bool {
	for key, value := range subMap {
		if value2, ok := map1[key]; !ok || value2 != value {
			return false
		}
	}
	return true
}
