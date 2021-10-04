package hello;

import org.joda.time.LocalTime;
import java.time.LocalTime;
public class HelloWorld {
    public static void main(String[] args) {
         LocalTime time = LocalTime.now();
        System.out.println(time + " - hello world");
    }
}
